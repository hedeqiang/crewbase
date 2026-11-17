package main

import (
	"strings"
	"sync/atomic"
)

// The desktop shell's own words (menu bar, tray, dialogs) in the UI's
// language: the page tells the shell (Desktop.SetLanguage) when it starts
// and whenever the person changes it; before that the system's language
// decides.

var uiLang atomic.Value // "zh" | "en"

func lang() string {
	if v, ok := uiLang.Load().(string); ok {
		return v
	}
	return systemLang()
}

// systemLang guesses from the environment (the page corrects it at once).
func systemLang() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := strings.ToLower(getenv(k)); v != "" {
			if strings.HasPrefix(v, "zh") {
				return "zh"
			}
			return "en"
		}
	}
	return "zh"
}

var words = map[string][2]string{ // key → {zh, en}
	"about":         {"关于 %s", "About %s"},
	"settings":      {"设置…", "Settings…"},
	"hide":          {"隐藏 %s", "Hide %s"},
	"quit":          {"退出 %s", "Quit %s"},
	"view":          {"显示", "View"},
	"window":        {"窗口", "Window"},
	"open":          {"打开 %s", "Open %s"},
	"needs":         {"需要你 (%d)", "Needs you (%d)"},
	"needsNone":     {"没有等你处理的事", "Nothing needs you"},
	"needsTip":      {"%s · %d 项需要你", "%s · %d need you"},
	"needsMore":     {"还有 %d 项…", "%d more…"},
	"atLogin":       {"开机时启动", "Open at login"},
	"keepRunning":   {"关闭窗口后继续运行", "Keep running when the window is closed"},
	"cuLive":        {"%s 正在操作 %s", "%s is operating %s"},
	"cuPaused":      {"%s 等你停下再操作 %s", "%s waits for you to stop before operating %s"},
	"cuStop":        {"停止电脑操作", "Stop computer use"},
	"notifyTest":    {"通知已开启：Agent 等你批准或完成任务时会在这里提醒你。", "Notifications are on: Crewbase tells you here when an agent needs you or finishes."},
	"updCheck":      {"检查更新…", "Check for Updates…"},
	"updRestart":    {"重启以完成更新", "Restart to Update"},
	"pickFolder":    {"选择项目文件夹", "Choose a project folder"},
	"pick":          {"选择", "Choose"},
	"pickFolderMsg": {"Agent 会在这个文件夹里读写文件、运行命令（按它的运行权限）。", "The agent reads, writes and runs commands in this folder, within its run mode."},
}

// tr returns the word for key in the UI's language.
func tr(key string) string {
	w, ok := words[key]
	if !ok {
		return key
	}
	if lang() == "en" {
		return w[1]
	}
	return w[0]
}

// mygoLang is the language tag of MyGo's own menus and the update window.
func mygoLang() string {
	if lang() == "en" {
		return "en"
	}
	return "zh-Hans"
}
