package xiaohongshu

import (
	"context"
	"log/slog"
	"math/rand"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// PublishArticleContent 发布长文内容
type PublishArticleContent struct {
	Title        string     // 标题，最大64字
	Content      string     // 正文内容
	Tags         []string   // 标签
	ScheduleTime *time.Time // 定时发布时间，nil 表示立即发布
	IsOriginal   bool       // 是否声明原创
	Visibility   string     // 可见范围: "公开可见"(默认), "仅自己可见", "仅互关好友可见"
	Products     []string   // 商品关键词列表，用于绑定带货商品
	TemplateName string     // 模板名称，如"简约基础"、"清晰明朗"等，为空则使用默认
}

const (
	// 写长文发布页面URL - 使用与图文/视频相同的发布页面，然后切换tab
	urlOfArticlePublish = `https://creator.xiaohongshu.com/publish/publish?source=official`
)

// PublishArticleAction 长文发布操作
type PublishArticleAction struct {
	page *rod.Page
}

type ArticlePublishResult struct {
	PostID  string
	PostURL string
}

// NewPublishArticleAction 进入写长文发布页
func NewPublishArticleAction(page *rod.Page) (*PublishArticleAction, error) {
	pp := page.Timeout(300 * time.Second)

	// 导航到发布页面
	if err := pp.Navigate(urlOfArticlePublish); err != nil {
		return nil, errors.Wrap(err, "导航到发布页面失败")
	}

	// 等待页面加载
	if err := pp.WaitLoad(); err != nil {
		logrus.Warnf("等待页面加载出现问题: %v，继续尝试", err)
	}
	randomHumanPause("页面加载后停顿", 2, 4)

	// 等待页面稳定
	if err := pp.WaitDOMStable(time.Second, 0.1); err != nil {
		logrus.Warnf("等待 DOM 稳定出现问题: %v，继续尝试", err)
	}
	randomHumanPause("DOM稳定后停顿", 2, 3)

	// 点击"写长文"tab 切换到长文发布模式
	if err := mustClickPublishTab(pp, "写长文"); err != nil {
		return nil, errors.Wrap(err, "切换到写长文失败")
	}

	randomHumanPause("切换写长文后停顿", 2, 4)

	// 点击"新的创作"按钮进入编辑页
	if err := clickNewCreation(pp); err != nil {
		logrus.Warnf("点击新的创作按钮失败: %v，可能已在编辑页", err)
	}

	randomHumanPause("进入创作页后停顿", 2, 4)

	return &PublishArticleAction{page: pp}, nil
}

// clickNewCreation 点击"新的创作"按钮（如果没有这个按钮则跳过）
func clickNewCreation(page *rod.Page) error {
	// 先尝试查找按钮，如果找不到就跳过
	_, err := page.Eval(`() => {
		const buttons = document.querySelectorAll('button');
		for (const btn of buttons) {
			if (btn.textContent.includes('新的创作') && btn.offsetParent !== null) {
				btn.click();
				return true;
			}
		}
		return false;
	}`)

	if err != nil {
		logrus.Warnf("点击新的创作按钮出错: %v，跳过", err)
		return nil // 找不到按钮不是错误
	}

	slog.Info("已点击新的创作按钮（或按钮不存在，已跳过）")
	return nil
}

// PublishArticle 发布长文
func (p *PublishArticleAction) PublishArticle(ctx context.Context, content PublishArticleContent) (*ArticlePublishResult, error) {
	page := p.page.Context(ctx)

	// 1. 输入标题和正文
	if err := inputArticleContent(page, content.Title, content.Content); err != nil {
		return nil, errors.Wrap(err, "输入长文内容失败")
	}

	// 2. 点击一键排版进入模板选择
	if err := clickOneKeyFormat(page); err != nil {
		return nil, errors.Wrap(err, "点击一键排版失败")
	}

	// 3. 选择模板（按 template_name 指定）
	if strings.TrimSpace(content.TemplateName) != "" {
		if err := selectTemplate(page, strings.TrimSpace(content.TemplateName)); err != nil {
			logrus.Warnf("选择模板失败: %v，继续使用当前模板", err)
		}
	}

	// 4. 点击下一步进入发布设置页
	if err := clickNextStep(page); err != nil {
		return nil, errors.Wrap(err, "点击下一步失败")
	}

	// 5. 在发布设置页填写信息
	if err := fillPublishSettings(page, content); err != nil {
		return nil, errors.Wrap(err, "填写发布设置失败")
	}

	// 6. 点击发布
	result, err := clickPublishButton(page)
	if err != nil {
		return nil, errors.Wrap(err, "点击发布按钮失败")
	}

	slog.Info("长文发布完成", "title", content.Title, "post_id", result.PostID, "post_url", result.PostURL)
	return result, nil
}

// inputArticleContent 输入长文标题和正文
func inputArticleContent(page *rod.Page, title, content string) error {
	// 等待编辑器加载
	if editor, err := page.Element(`.tiptap.ProseMirror, .ProseMirror`); err == nil {
		_ = editor.WaitVisible()
	}

	// 输入标题
	titleInput, err := page.Element(`textarea[placeholder*="标题"], input[placeholder*="标题"]`)
	if err != nil {
		return errors.Wrap(err, "查找标题输入框失败")
	}

	_ = titleInput.SelectAllText()
	if err := titleInput.Input(title); err != nil {
		return errors.Wrap(err, "输入标题失败")
	}
	slog.Info("已输入标题", "title", title)
	randomHumanPause("输入标题后停顿", 2, 3)

	// 输入正文 - 使用 ProseMirror 编辑器
	editor, err := page.Element(`.tiptap.ProseMirror, .ProseMirror`)
	if err != nil {
		return errors.Wrap(err, "查找正文编辑器失败")
	}

	// 点击编辑器获取焦点
	if err := editor.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "点击编辑器失败")
	}
	randomHumanPause("聚焦正文编辑器后停顿", 1, 2)

	// 使用 JavaScript 设置编辑器内容
	_, err = editor.Eval(`(content) => {
		this.focus();
		// 将换行符转换为段落
		const paragraphs = content.split('\n').filter(p => p.trim() !== '');
		this.innerHTML = paragraphs.map(p => '<p>' + p + '</p>').join('');
		// 触发输入事件
		this.dispatchEvent(new Event('input', { bubbles: true }));
		this.dispatchEvent(new Event('change', { bubbles: true }));
		return true;
	}`, content)
	if err != nil {
		return errors.Wrap(err, "输入正文失败")
	}

	slog.Info("已输入正文", "length", len(content))
	randomHumanPause("输入正文后停顿", 2, 3)

	return nil
}

// clickOneKeyFormat 点击一键排版按钮
func clickOneKeyFormat(page *rod.Page) error {
	if err := clickVisibleButtonByText(page, "一键排版"); err != nil {
		return errors.Wrap(err, "点击一键排版按钮失败")
	}

	slog.Info("已点击一键排版")
	randomHumanPause("等待模板选择页加载", 3, 5)

	return nil
}

// selectTemplate 选择模板
func selectTemplate(page *rod.Page, templateName string) error {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		result, err := page.Eval(`(name) => {
			const cards = Array.from(document.querySelectorAll('.template-card'));
			for (const card of cards) {
				if (!card || card.offsetParent === null) continue;
				const text = (card.textContent || '').trim();
				if (text !== name) continue;

				card.scrollIntoView({ block: 'center', inline: 'center' });
				card.click();
				const selected = card.classList.contains('selected');
				return {found: true, selected};
			}
			return {found: false, selected: false};
		}`, templateName)
		if err == nil && result.Value.Get("found").Bool() {
			if result.Value.Get("selected").Bool() {
				slog.Info("已选择模板", "template", templateName)
				randomHumanPause("选择模板后停顿", 2, 4)
				return nil
			}

			// 若第一次点击后未立即 selected，短暂等待再复检
			time.Sleep(600 * time.Millisecond)
			verify, vErr := page.Eval(`(name) => {
				const cards = Array.from(document.querySelectorAll('.template-card.selected'));
				return cards.some(c => (c.textContent || '').trim() === name);
			}`, templateName)
			if vErr == nil && verify.Value.Bool() {
				slog.Info("已选择模板", "template", templateName)
				randomHumanPause("选择模板后停顿", 2, 4)
				return nil
			}
		}

		time.Sleep(500 * time.Millisecond)
	}

	return errors.Errorf("未找到或未成功选中模板: %s", templateName)
}

// selectTemplate 选择模板（简化版本，使用第一个模板）
func selectFirstTemplate(page *rod.Page) error {
	// 查找第一个模板
	templates, err := page.Elements(`.template-item, .template-card, [class*="template"]`)
	if err != nil || len(templates) == 0 {
		return errors.New("未找到模板")
	}

	if err := templates[0].Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errors.Wrap(err, "点击模板失败")
	}

	slog.Info("已选择第一个模板")
	time.Sleep(500 * time.Millisecond)

	return nil
}

// clickNextStep 点击下一步按钮
func clickNextStep(page *rod.Page) error {
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		// 先清理可能的遮挡层，避免点击被拦截
		removePopCover(page)

		result, err := page.Eval(`() => {
			const buttons = Array.from(document.querySelectorAll('button'))
				.filter(btn => btn && btn.offsetParent !== null && (btn.textContent || '').trim() === '下一步');
			if (buttons.length === 0) return { clicked: false, reason: 'not_found' };

			const btn = buttons[0];
			const disabled =
				btn.disabled ||
				btn.getAttribute('disabled') !== null ||
				btn.getAttribute('aria-disabled') === 'true' ||
				btn.classList.contains('disabled');
			if (disabled) return { clicked: false, reason: 'disabled' };

			btn.scrollIntoView({ block: 'center', inline: 'center' });
			const rect = btn.getBoundingClientRect();
			const x = rect.left + rect.width / 2;
			const y = rect.top + rect.height / 2;
			const top = document.elementFromPoint(x, y);
			if (!(top === btn || btn.contains(top))) {
				return { clicked: false, reason: 'blocked' };
			}

			['pointerdown', 'mousedown', 'pointerup', 'mouseup', 'click'].forEach(type => {
				btn.dispatchEvent(new MouseEvent(type, { bubbles: true, cancelable: true, view: window }));
			});
			return { clicked: true, reason: 'ok' };
		}`)
		if err != nil {
			time.Sleep(300 * time.Millisecond)
			continue
		}

		if result.Value.Get("clicked").Bool() {
			slog.Info("已点击下一步")
			randomHumanPause("点击下一步后停顿", 2, 4)
			// 点击成功后，必须等待进入发布设置页
			if err := waitForArticlePublishSettingsPage(page, 6*time.Second); err == nil {
				return nil
			}
		}

		time.Sleep(400 * time.Millisecond)
	}

	return errors.New("点击下一步失败：按钮可能被遮挡、未启用或未触发页面跳转")
}

// fillPublishSettings 填写发布设置
func fillPublishSettings(page *rod.Page, content PublishArticleContent) error {
	// 双重校验，确保当前确实在发布设置页，避免误在模板页继续执行
	if err := waitForArticlePublishSettingsPage(page, 15*time.Second); err != nil {
		return errors.Wrap(err, "未进入长文发布设置页")
	}

	// 输入标题（发布页可能需要重新输入或确认）
	if err := fillTitleIfNeeded(page, content.Title); err != nil {
		logrus.Warnf("填写标题失败: %v", err)
	}

	// 输入正文描述
	if err := fillContentDescription(page, content.Content); err != nil {
		logrus.Warnf("填写正文描述失败: %v", err)
	}

	// 输入话题标签
	if len(content.Tags) > 0 {
		if err := inputArticleTags(page, content.Tags); err != nil {
			logrus.Warnf("输入话题标签失败: %v", err)
		}
	}

	// 设置定时发布
	if content.ScheduleTime != nil {
		if err := setArticleSchedulePublish(page, *content.ScheduleTime); err != nil {
			return errors.Wrap(err, "设置定时发布失败")
		}
	}

	// 设置可见范围
	if err := setArticleVisibility(page, content.Visibility); err != nil {
		return errors.Wrap(err, "设置可见范围失败")
	}

	// 设置原创声明
	if content.IsOriginal {
		if err := setArticleOriginal(page); err != nil {
			logrus.Warn("设置原创声明失败", "error", err)
		}
	}

	// 绑定商品
	if len(content.Products) > 0 {
		if err := bindArticleProducts(page, content.Products); err != nil {
			return errors.Wrap(err, "绑定商品失败")
		}
	}

	return nil
}

// fillTitleIfNeeded 如有需要填写标题
func fillTitleIfNeeded(page *rod.Page, title string) error {
	// 查找标题输入框
	titleInput, err := page.Element(`textarea[placeholder*="标题"], input[placeholder*="标题"], input[placeholder*="赞哦"]`)
	if err != nil {
		return err
	}

	// 检查当前值
	currentVal, err := titleInput.Eval(`() => (this.value || "").trim()`)
	if err != nil {
		return err
	}

	// 如果标题已存在且不为空，则不覆盖
	if currentVal.Value.String() != "" {
		return nil
	}

	_ = titleInput.SelectAllText()
	return titleInput.Input(title)
}

// fillContentDescription 填写正文描述
func fillContentDescription(page *rod.Page, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}

	contentElem, err := findArticleContentElement(page)
	if err != nil || contentElem == nil {
		// 正文框不可用时不硬失败，交给后续发布校验兜底
		logrus.Warnf("未找到长文发布页正文输入框: %v", err)
		return nil
	}

	if err := contentElem.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond)

	// 清空已有内容，避免正文累加
	_ = contentElem.SelectAllText()
	_ = page.Keyboard.Press(input.Backspace)
	time.Sleep(100 * time.Millisecond)

	return contentElem.Input(content)
}

// inputArticleTags 输入话题标签
func inputArticleTags(page *rod.Page, tags []string) error {
	if len(tags) == 0 {
		return nil
	}

	// 限制标签数量
	if len(tags) > 10 {
		logrus.Warnf("标签数量超过10，截取前10个标签")
		tags = tags[:10]
	}

	contentElem, err := findArticleContentElement(page)
	if err != nil || contentElem == nil {
		return errors.Wrap(err, "未找到长文发布页内容输入框")
	}

	// 先点击内容区域
	if err := contentElem.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)

	// 移动到内容末尾
	for i := 0; i < 5; i++ {
		err := page.Keyboard.Press(input.End)
		if err != nil {
			logrus.Warnf("按End键失败: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 复用图文流程的稳定标签输入实现
	return inputTags(contentElem, tags)
}

func findArticleContentElement(page *rod.Page) (*rod.Element, error) {
	selectors := []string{
		`div[role="textbox"][contenteditable="true"]`,
		`div.tiptap.ProseMirror[contenteditable="true"]`,
		`.ql-editor`,
		`div[contenteditable="true"]`,
	}

	for _, sel := range selectors {
		el, err := page.Element(sel)
		if err != nil || el == nil {
			continue
		}
		if err := el.WaitVisible(); err != nil {
			continue
		}

		placeholder, _ := el.Attribute("placeholder")
		dataPlaceholder, _ := el.Attribute("data-placeholder")
		if placeholder != nil && strings.Contains(*placeholder, "标题") {
			continue
		}
		if dataPlaceholder != nil && strings.Contains(*dataPlaceholder, "标题") {
			continue
		}
		return el, nil
	}

	return nil, errors.New("no contenteditable textbox found")
}

// setArticleSchedulePublish 设置定时发布
func setArticleSchedulePublish(page *rod.Page, t time.Time) error {
	return setSchedulePublish(page, t)
}

// setArticleVisibility 设置可见范围
func setArticleVisibility(page *rod.Page, visibility string) error {
	return setVisibility(page, visibility)
}

// setArticleOriginal 设置原创声明
func setArticleOriginal(page *rod.Page) error {
	return setOriginal(page)
}

// bindArticleProducts 绑定商品
func bindArticleProducts(page *rod.Page, products []string) error {
	return bindProducts(page, products)
}

// clickPublishButton 点击发布按钮
func clickPublishButton(page *rod.Page) (*ArticlePublishResult, error) {
	if submitButton, err := page.Element(".publish-page-publish-btn button.bg-red"); err == nil {
		randomHumanPause("点击发布前停顿", 2, 4)
		if err := submitButton.Click(proto.InputMouseButtonLeft, 1); err != nil {
			return nil, errors.Wrap(err, "点击发布按钮失败")
		}
		slog.Info("已点击发布按钮")
		return waitForArticlePublishSuccess(page, 20*time.Second)
	}

	if err := clickVisibleButtonByText(page, "发布"); err != nil {
		return nil, errors.Wrap(err, "点击发布按钮失败")
	}

	slog.Info("已点击发布按钮")
	return waitForArticlePublishSuccess(page, 20*time.Second)
}

func clickVisibleButtonByText(page *rod.Page, text string) error {
	randomHumanPause("准备点击按钮:"+text, 2, 4)
	result, err := page.Eval(`(label) => {
		const buttons = document.querySelectorAll('button');
		for (const btn of buttons) {
			if (!btn || btn.offsetParent === null) continue;
			if ((btn.textContent || '').trim() !== label) continue;
			btn.click();
			return true;
		}
		return false;
	}`, text)
	if err != nil {
		return err
	}
	if !result.Value.Bool() {
		return errors.Errorf("未找到按钮: %s", text)
	}
	randomHumanPause("点击按钮后停顿:"+text, 1, 2)
	return nil
}

func randomHumanPause(action string, minSeconds, maxSeconds int) {
	if minSeconds <= 0 {
		minSeconds = 1
	}
	if maxSeconds < minSeconds {
		maxSeconds = minSeconds
	}

	delay := minSeconds
	if maxSeconds > minSeconds {
		delay += rand.New(rand.NewSource(time.Now().UnixNano())).Intn(maxSeconds - minSeconds + 1)
	}

	d := time.Duration(delay) * time.Second
	slog.Info("模拟人工停顿", "action", action, "delay", d.String())
	time.Sleep(d)
}

func waitForArticlePublishSettingsPage(page *rod.Page, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := page.Eval(`() => {
			const titleInput = document.querySelector('input[placeholder*="填写标题"], input[placeholder*="赞哦"], textarea[placeholder*="填写标题"]');
			const publishBtn = document.querySelector('.publish-page-publish-btn button, button');
			const bodyText = (document.body && document.body.innerText) || '';

			const hasPublishText = publishBtn && (publishBtn.textContent || '').includes('发布');
			const hasRightPanelSignals = bodyText.includes('内容设置') || bodyText.includes('更多设置');
			return Boolean(titleInput) && (Boolean(hasPublishText) || hasRightPanelSignals);
		}`)
		if err == nil && result.Value.Bool() {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}

	info, _ := page.Info()
	return errors.Errorf("等待发布设置页超时，当前URL: %s", info.URL)
}

func waitForArticlePublishSuccess(page *rod.Page, timeout time.Duration) (*ArticlePublishResult, error) {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if info, err := page.Info(); err == nil && strings.Contains(info.URL, "/publish/success") {
			result := extractArticlePublishResult(page, info.URL)
			slog.Info("检测到长文发布成功页", "url", info.URL, "post_id", result.PostID)
			return result, nil
		}

		result, err := page.Eval(`() => {
			const bodyText = (document.body && document.body.innerText) || '';
			return bodyText.includes('发布成功') || bodyText.includes('立即返回');
		}`)
		if err == nil && result.Value.Bool() {
			info, _ := page.Info()
			result := extractArticlePublishResult(page, info.URL)
			slog.Info("检测到长文发布成功文案", "post_id", result.PostID, "url", result.PostURL)
			return result, nil
		}

		time.Sleep(400 * time.Millisecond)
	}

	info, _ := page.Info()
	return nil, errors.Errorf("发布后未检测到成功页，当前URL: %s", info.URL)
}

func extractArticlePublishResult(page *rod.Page, currentURL string) *ArticlePublishResult {
	result := &ArticlePublishResult{PostURL: currentURL}
	if id := extractPostIDFromURL(currentURL); id != "" {
		result.PostID = id
		return result
	}

	jsResult, err := page.Eval(`() => {
		const hrefs = Array.from(document.querySelectorAll('a[href]'))
			.map(a => a.href)
			.filter(Boolean);
		return {
			location: window.location.href || '',
			hrefs,
		};
	}`)
	if err != nil {
		return result
	}

	loc := jsResult.Value.Get("location").String()
	if loc != "" {
		result.PostURL = loc
		if id := extractPostIDFromURL(loc); id != "" {
			result.PostID = id
			return result
		}
	}

	hrefs := jsResult.Value.Get("hrefs").Arr()
	for _, h := range hrefs {
		href := h.String()
		if result.PostURL == "" && href != "" {
			result.PostURL = href
		}
		if id := extractPostIDFromURL(href); id != "" {
			result.PostID = id
			result.PostURL = href
			return result
		}
	}

	return result
}

func extractPostIDFromURL(raw string) string {
	if raw == "" {
		return ""
	}

	u, err := url.Parse(raw)
	if err == nil {
		for _, key := range []string{"post_id", "note_id", "id", "postId", "noteId"} {
			if v := u.Query().Get(key); v != "" {
				return v
			}
		}
	}

	// 小红书常见笔记链接形如 /explore/{noteId}
	re := regexp.MustCompile(`/explore/([a-zA-Z0-9]+)`)
	matches := re.FindStringSubmatch(raw)
	if len(matches) >= 2 {
		return matches[1]
	}

	return ""
}
