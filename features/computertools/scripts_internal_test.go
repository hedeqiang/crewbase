package computertools

import (
	"slices"
	"strings"
	"testing"
)

func TestScriptApps(t *testing.T) {
	for _, c := range []struct {
		src  string
		apps []string
		dyn  bool
	}{
		{`tell application "Notes" to make new note with properties {body:"x"}`, []string{"Notes"}, false},
		{"tell app \"Finder\"\n  get name of every window\nend tell", []string{"Finder"}, false},
		{`TELL APPLICATION "Mail" to get subject of messages of inbox`, []string{"Mail"}, false},
		{`tell application id "com.apple.Terminal" to activate`, []string{"com.apple.Terminal"}, false},
		{`tell application "Terminal.app" to do script "ls"`, []string{"Terminal"}, false},
		{`tell application "/Applications/Utilities/Terminal.app" to activate`, []string{"Terminal"}, false},
		{`using terms from application "Mail"` + "\n" + `tell application "Notes" to get name of notes` + "\n" + `tell application "Notes" to activate`, []string{"Mail", "Notes"}, false},
		{`tell application "System Events" to tell process "Keychain Access" to get name of windows`, []string{"System Events", "Keychain Access"}, false},
		{`tell application "System Events" to tell application process "1Password" to get windows`, []string{"System Events", "1Password"}, false},
		{`Application("Notes").notes.name()`, []string{"Notes"}, false},
		{`const m = Application('com.apple.mail'); m.inbox.messages.subject()`, []string{"com.apple.mail"}, false},
		{"Application(`Music`).currentTrack.name()", []string{"Music"}, false},
		{`Application ( "Calendar" ).calendars.name()`, []string{"Calendar"}, false},
		{`Application("System Events").processes["Terminal"].windows.name()`, []string{"System Events", "Terminal"}, false},
		{`Application("System Events").processes.byName('Passwords').windows()`, []string{"System Events", "Passwords"}, false},
		// the script itself, no app
		{`return 1 + 1`, nil, false},
		{"var app = Application.currentApplication(); app.includeStandardAdditions = true; app.displayDialog('hi')", nil, false},
		{`tell current application to display dialog "hi"`, nil, false},
		{`set p to path to application support from user domain`, nil, false},
		// targets picked at run time
		{`set a to "Terminal"` + "\n" + `tell application a to activate`, nil, true},
		{`tell application ("Term" & "inal") to activate`, nil, true},
		{`tell application id someId to activate`, nil, true},
		{`get name of application (x)`, nil, true},
		{`const n = "Terminal"; Application(n).activate()`, nil, true},
		{`Application(["Ter","minal"].join("")).activate()`, nil, true},
	} {
		apps, dyn := scriptApps(c.src)
		if !slices.Equal(apps, c.apps) || dyn != c.dyn {
			t.Errorf("scriptApps(%q) = %q, %v; want %q, %v", c.src, apps, dyn, c.apps, c.dyn)
		}
	}
}

func TestScriptRefusal(t *testing.T) {
	for _, src := range []string{
		`do shell script "rm -rf ~"`,
		"tell application \"Finder\"\n  DO   SHELL\tSCRIPT \"ls\"\nend tell",
		`set x to do shell script "id" with administrator privileges`,
		`var app = Application.currentApplication(); app.includeStandardAdditions = true; app.doShellScript("ls")`,
		`app["doShellScript"]("ls")`,
		`«event sysoexec» "ls"`,
		`ObjC.import("Foundation"); $.NSTask.launchedTaskWithLaunchPathArguments("/bin/ls", [])`,
		`use framework "Foundation"` + "\n" + `current application's NSTask's launchedTaskWithLaunchPath:"/bin/sh" arguments:{}`,
		`ObjC.import('stdlib'); $.system("ls")`,
		`$.popen("ls", "r")`,
		`run script "do sh" & "ell script \"ls\""`,
		`eval("app.do" + "ShellScript('ls')")`,
		`new Function("return 1")()`,
		`tell application "System Events" to keystroke "v" using command down`,
		`tell application "System Events" to key code 36`,
		`Application("System Events").keystroke("hello")`,
		`Application("System Events").keyCode(36)`,
		`-- just a comment: do shell script`,
	} {
		if scriptRefusal(src) == "" {
			t.Errorf("not refused: %q", src)
		}
	}
	if why := scriptRefusal(`do shell script "ls"`); !strings.Contains(why, "shell tool") {
		t.Errorf("refusal does not point to the shell tool: %q", why)
	}
	if why := scriptRefusal(`tell application "System Events" to keystroke "a"`); !strings.Contains(why, "computer_act") {
		t.Errorf("keystroke refusal does not point to computer_act: %q", why)
	}
	for _, src := range []string{
		`tell application "Notes" to make new note with properties {name:"Shell scripts", body:"a script"}`,
		`tell application "Music" to play`,
		`Application("Reminders").lists.byName("Inbox").reminders.name()`,
		`tell application "Finder" to get name of every file of desktop`,
		`return 1+1`,
		`set evaluation to "fine"`,
	} {
		if why := scriptRefusal(src); why != "" {
			t.Errorf("refused %q: %s", src, why)
		}
	}
}

func TestScriptLang(t *testing.T) {
	for in, want := range map[string]string{"applescript": "applescript", "AppleScript": "applescript", "": "applescript",
		"javascript": "javascript", "JXA": "javascript", "js": "javascript", "python": "", "sh": ""} {
		if got := scriptLang(in); got != want {
			t.Errorf("scriptLang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFenced(t *testing.T) {
	if got := fenced("applescript", "return 1"); got != "```applescript\nreturn 1\n```" {
		t.Fatalf("fenced = %q", got)
	}
	if got := fenced("", "a ``` b"); !strings.HasPrefix(got, "````\n") || !strings.HasSuffix(got, "\n````") {
		t.Fatalf("fence closed by the content: %q", got)
	}
}
