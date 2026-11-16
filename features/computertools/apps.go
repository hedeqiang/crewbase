package computertools

import (
	"strings"
)

// Known app groups. An app matches an entry by bundle id or by name
// (case-insensitive, any listed alias), so an app that is not running yet
// ("open Terminal") is recognised too.
type known struct {
	Group string   // see the Group* constants
	IDs   []string // bundle identifiers
	Names []string // names people see, in English and Chinese
}

// Groups of the default block list (and of browsers and messaging apps).
const (
	GroupPasswords = "passwords" // keychain and password managers
	GroupTerminal  = "terminal"  // shells: anything typed there runs as the person
	GroupSystem    = "system"    // System Settings: privacy, accounts, security
	GroupSelf      = "crewbase"  // Crewbase itself: an agent must not approve its own requests
	GroupFinance   = "finance"   // banks, brokers, crypto wallets and exchanges
	GroupBrowser   = "browser"   // websites go to the browser_* tools
)

// blockedByDefault are never operated (not even asked about) until the
// person unblocks one in Settings › 电脑操作. The finance list is the
// common desktop apps in China and abroad, matched mostly by name; it is
// a floor, not an inventory: people block others in settings.
var blockedByDefault = []known{
	{GroupPasswords, []string{"com.apple.keychainaccess"}, []string{"Keychain Access", "钥匙串访问"}},
	{GroupPasswords, []string{"com.apple.Passwords"}, []string{"Passwords", "密码"}},
	{GroupPasswords, []string{"com.1password.1password", "com.agilebits.onepassword7", "com.agilebits.onepassword-osx"}, []string{"1Password", "1Password 7", "1Password 8"}},
	{GroupPasswords, nil, []string{"Bitwarden"}},
	{GroupPasswords, nil, []string{"Dashlane"}},
	{GroupPasswords, []string{"com.lastpass.LastPass"}, []string{"LastPass"}},
	{GroupPasswords, []string{"in.sinew.Enpass-Desktop"}, []string{"Enpass"}},
	{GroupPasswords, []string{"com.keepersecurity.passwordmanager"}, []string{"Keeper", "Keeper Password Manager"}},

	{GroupTerminal, []string{"com.apple.Terminal"}, []string{"Terminal", "终端"}},
	{GroupTerminal, []string{"com.googlecode.iterm2"}, []string{"iTerm2", "iTerm"}},
	{GroupTerminal, []string{"dev.warp.Warp-Stable", "dev.warp.Warp"}, []string{"Warp"}},
	{GroupTerminal, []string{"com.mitchellh.ghostty"}, []string{"Ghostty"}},
	{GroupTerminal, []string{"net.kovidgoyal.kitty"}, []string{"kitty"}},
	{GroupTerminal, []string{"com.github.wez.wezterm"}, []string{"WezTerm"}},
	{GroupTerminal, []string{"org.alacritty", "io.alacritty"}, []string{"Alacritty"}},

	{GroupSystem, []string{"com.apple.systempreferences"}, []string{"System Settings", "System Preferences", "系统设置", "系统偏好设置"}},

	{GroupSelf, []string{"app.crewbase.desktop"}, []string{"Crewbase"}},

	// crypto wallets and exchanges
	{GroupFinance, []string{"com.ledger.live"}, []string{"Ledger Live"}},
	{GroupFinance, []string{"io.trezor.TrezorSuite"}, []string{"Trezor Suite"}},
	{GroupFinance, []string{"org.electrum.electrum"}, []string{"Electrum"}},
	{GroupFinance, nil, []string{"Exodus", "Binance", "币安", "OKX", "欧易", "Coinbase", "Kraken", "Bitget", "Bybit", "MetaMask", "imToken"}},
	// brokers
	{GroupFinance, []string{"cn.futu.Niuniu"}, []string{"Futu NiuNiu", "富途牛牛", "moomoo"}},
	{GroupFinance, nil, []string{"Tiger Trade", "老虎证券", "同花顺", "东方财富", "通达信", "Trader Workstation", "IBKR Desktop", "thinkorswim", "Webull", "长桥", "Longbridge"}},
	// banks and payments
	{GroupFinance, nil, []string{"支付宝", "Alipay", "PayPal", "Wise", "Revolut", "招商银行", "中国工商银行", "工商银行", "中国建设银行", "建设银行", "中国银行", "中国农业银行", "农业银行", "交通银行", "浦发银行"}},
}

// browsers: the agent's own browser (browser_* tools) keeps its logins and
// is safer and cheaper than operating the person's.
var browsers = []known{
	{GroupBrowser, []string{"com.apple.Safari", "com.apple.SafariTechnologyPreview"}, []string{"Safari", "Safari Technology Preview"}},
	{GroupBrowser, []string{"com.google.Chrome", "com.google.Chrome.beta", "com.google.Chrome.dev", "com.google.Chrome.canary", "org.chromium.Chromium", "com.google.chrome.for.testing"}, []string{"Google Chrome", "Chrome", "Chromium", "Google Chrome for Testing"}},
	{GroupBrowser, []string{"company.thebrowser.Browser", "company.thebrowser.dia"}, []string{"Arc", "Dia"}},
	{GroupBrowser, []string{"com.microsoft.edgemac", "com.microsoft.edgemac.Beta", "com.microsoft.edgemac.Dev"}, []string{"Microsoft Edge", "Edge"}},
	{GroupBrowser, []string{"org.mozilla.firefox", "org.mozilla.firefoxdeveloperedition", "org.mozilla.nightly"}, []string{"Firefox", "Firefox Developer Edition", "Firefox Nightly"}},
	{GroupBrowser, []string{"com.brave.Browser"}, []string{"Brave Browser", "Brave"}},
	{GroupBrowser, []string{"com.operasoftware.Opera"}, []string{"Opera"}},
	{GroupBrowser, []string{"com.vivaldi.Vivaldi"}, []string{"Vivaldi"}},
}

// messaging are mail and chat apps, where return in a text field sends.
var messaging = []known{
	{"mail", []string{"com.apple.mail", "com.microsoft.Outlook", "com.readdle.smartemail-Mac", "org.mozilla.thunderbird", "com.mimestream.Mimestream", "it.bloop.airmail2"}, []string{"Mail", "邮件", "Microsoft Outlook", "Outlook", "Spark", "Thunderbird", "Mimestream", "Airmail"}},
	{"chat", []string{"com.apple.MobileSMS", "com.tencent.xinWeChat", "com.tencent.qq", "com.tencent.WeWorkMac", "com.electron.lark", "com.bytedance.lark", "com.alibaba.DingTalkMac", "com.tinyspeck.slackmacgap", "ru.keepcoder.Telegram", "org.telegram.desktop", "com.hnc.Discord", "net.whatsapp.WhatsApp", "com.microsoft.teams2", "com.microsoft.teams", "org.whispersystems.signal-desktop", "jp.naver.line.mac", "us.zoom.xos"},
		[]string{"Messages", "信息", "WeChat", "微信", "QQ", "企业微信", "WeCom", "飞书", "Feishu", "Lark", "钉钉", "DingTalk", "Slack", "Telegram", "Discord", "WhatsApp", "Microsoft Teams", "Signal", "LINE", "zoom.us"}},
}

func (k known) matches(id, name string) bool {
	for _, x := range k.IDs {
		if id != "" && strings.EqualFold(x, id) {
			return true
		}
	}
	for _, x := range k.Names {
		if (name != "" && strings.EqualFold(x, name)) || (id != "" && strings.EqualFold(x, id)) {
			return true
		}
	}
	return false
}

func find(list []known, id, name string) (known, bool) {
	for _, k := range list {
		if k.matches(id, name) {
			return k, true
		}
	}
	return known{}, false
}

// defaultBlocked reports the default-block group an app belongs to.
// defaultBlocked reports the default-block group of an app. Browsers are
// in it too: websites go to the browser_* tools unless the person allows a
// browser in Settings › 电脑操作 (e.g. to use their own logged-in browser).
func defaultBlocked(id, name string) (string, bool) {
	if k, ok := find(blockedByDefault, id, name); ok {
		return k.Group, true
	}
	k, ok := find(browsers, id, name)
	return k.Group, ok
}

// IsBrowser reports whether the app is a web browser.
func IsBrowser(id, name string) bool {
	_, ok := find(browsers, id, name)
	return ok
}

// isMessaging reports whether return in a text field of the app sends.
func isMessaging(id, name string) bool {
	_, ok := find(messaging, id, name)
	return ok
}

// BlockedEntry is one row of the default block list for settings.
type BlockedEntry struct {
	ID    string `json:"id"` // bundle id ("" when matched by name only)
	Name  string `json:"name"`
	Group string `json:"group"`
}

// DefaultBlocked lists the default block list, one row per app.
func DefaultBlocked() []BlockedEntry {
	var out []BlockedEntry
	for _, k := range append(append([]known(nil), blockedByDefault...), browsers...) {
		id := ""
		if len(k.IDs) > 0 {
			id = k.IDs[0]
		}
		if id != "" {
			out = append(out, BlockedEntry{ID: id, Name: k.Names[0], Group: k.Group})
			continue
		}
		for _, n := range k.Names {
			out = append(out, BlockedEntry{Name: n, Group: k.Group})
		}
	}
	return out
}

// entryKey is how a default entry is unblocked: its first bundle id, or
// "name:<name>" for name-only entries.
func entryKey(id, name string) string {
	k, ok := find(blockedByDefault, id, name)
	if !ok {
		k, ok = find(browsers, id, name)
	}
	if ok {
		if len(k.IDs) > 0 {
			return k.IDs[0]
		}
		for _, n := range k.Names {
			if strings.EqualFold(n, name) || strings.EqualFold(n, id) {
				return "name:" + n
			}
		}
	}
	return id
}
