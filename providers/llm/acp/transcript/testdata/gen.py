# Writes synthetic transcript fixtures (no real data) for the transcript
# package tests. Hashes are computed here, independently of the Go code.
import json,hashlib,os,sys
root=sys.argv[1]
SID="11111111-2222-4333-8444-555555555555"
OTHER="99999999-2222-4333-8444-555555555555"
def H(role,content): return hashlib.sha256(json.dumps({"role":role,"content":content},separators=(",",":"),sort_keys=True,ensure_ascii=False).encode()).hexdigest()
def line(o): return json.dumps(o,ensure_ascii=False,separators=(",",":"))
L=[]
def add(o,sid=SID):
    o=dict(o); o.setdefault("sessionId",sid); L.append(line(o))
shape={"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},
                 {"type":"text","text":"# Environment\nWorking directory: /tmp/proj\nToken: "+"sk-"+"fixture"+"Q"*24+"\n"}],
       "tools":[{"name":"Bash","description":"Run a shell command.","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}},
                {"name":"Read","description":"Read a file.","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}},
                {"name":"mcp__crewbase__computer_screenshot","description":"Take a screenshot.","input_schema":{"type":"object","properties":{}}},
                {"name":"mcp__github__create_issue","description":"Create an issue.","input_schema":{"type":"object","properties":{"title":{"type":"string"}}},"defer_loading":True}]}
add({"type":"queue-operation","operation":"enqueue","timestamp":"2026-11-17T10:00:00.000Z"})
add({"type":"api-request-shape","shapeHash":"s1","shape":shape,"timestamp":"2026-11-17T10:00:00.100Z","version":"2.1.0"})
u1={"role":"user","content":"Fix the bug in main.go"}
add({"type":"user","isSidechain":False,"uuid":"u1","message":u1,"timestamp":"2026-11-17T10:00:00.200Z"})
add({"type":"api-request","id":"r1","querySource":"sdk","shapeHash":"s1","params":{"model":"claude-opus-4-5","max_tokens":32000},"tail":[H("user",u1["content"])],"timestamp":"2026-11-17T10:00:00.300Z"})
a1b0={"type":"thinking","thinking":"Let me read main.go first.","signature":"sig"}
a1b1={"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"main.go"}}
usage1={"input_tokens":3,"cache_read_input_tokens":0,"cache_creation_input_tokens":5000,"output_tokens":120}
add({"type":"assistant","isSidechain":False,"requestRef":"r1","apiBlockIndex":0,"message":{"id":"msg_1","role":"assistant","model":"claude-opus-4-5","content":[a1b0],"usage":usage1}})
add({"type":"assistant","isSidechain":False,"requestRef":"r1","apiBlockIndex":1,"message":{"id":"msg_1","role":"assistant","model":"claude-opus-4-5","content":[a1b1],"usage":usage1}})
u2c=[{"type":"tool_result","tool_use_id":"toolu_1","content":"package main\n// <script>alert('x')</script> & done\nfunc main() {}\n"}]
add({"type":"user","isSidechain":False,"message":{"role":"user","content":u2c}})
add({"type":"api-request","id":"r2","querySource":"sdk","shapeHash":"s1","params":{"model":"claude-opus-4-5"},"base":"r1","keep":1,"tail":[H("assistant",[a1b0,a1b1]),H("user",u2c)],"timestamp":"2026-11-17T10:00:01.000Z"})
# a side request (compaction) and a title: skipped
b1={"role":"user","content":"Summarize the conversation."}
add({"type":"api-request-blob","hash":H("user",b1["content"]),"message":b1})
add({"type":"api-request","id":"rside","querySource":"compact","shapeHash":"s1","params":{"model":"claude-haiku-4-5"},"base":"r2","keep":3,"tail":[H("user",b1["content"])],"timestamp":"2026-11-17T10:00:01.500Z"})
add({"type":"ai-title","aiTitle":"Fix main.go"})
a2c=[{"type":"text","text":"Found it. 修复完成 ✓"}]
add({"type":"assistant","isSidechain":False,"requestRef":"r2","apiBlockIndex":0,"message":{"id":"msg_2","role":"assistant","model":"claude-opus-4-5","content":a2c,"usage":{"input_tokens":2,"cache_read_input_tokens":5000,"cache_creation_input_tokens":300,"output_tokens":50}}})
# a sub-agent's line in the main file: not part of the main thread
add({"type":"assistant","isSidechain":True,"requestRef":"rsub","message":{"id":"msg_sub","role":"assistant","content":[{"type":"text","text":"sidechain"}],"usage":{"input_tokens":1,"output_tokens":1}}})
b2c=[{"type":"text","text":"<system-reminder>\nThe user said \"go on\" line sep\ttab\u0001ctl\n</system-reminder>"}]
add({"type":"api-request-blob","hash":H("system",b2c),"message":{"role":"system","content":b2c}})
add({"type":"api-request","id":"r4","querySource":"sdk","shapeHash":"s1","params":{"model":"claude-opus-4-5"},"base":"r2","keep":3,"tail":[H("assistant",a2c),H("system",b2c),"0"*64],"timestamp":"2026-11-17T10:00:02.000Z"})
add({"type":"assistant","isSidechain":False,"requestRef":"r4","apiBlockIndex":0,"message":{"id":"msg_3","role":"assistant","model":"claude-opus-4-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":4,"cache_read_input_tokens":5300,"cache_creation_input_tokens":120,"output_tokens":10}}})
# another session's lines in the same file: never read
add({"type":"api-request-shape","shapeHash":"s9","shape":{"system":[{"type":"text","text":"OTHER SESSION"}],"tools":[]}},OTHER)
add({"type":"api-request","id":"r9","querySource":"sdk","shapeHash":"s9","params":{"model":"other"},"tail":[]},OTHER)
d=os.path.join(root,"claude","projects","-tmp-proj"); os.makedirs(d,exist_ok=True)
with open(os.path.join(d,SID+".jsonl"),"w") as w:
    w.write("\n".join(L)+"\n")
    w.write('{"type":"api-request","id":"rpartial","querySource":"sdk","sessionId":"'+SID+'"')  # being written: no newline
# a sub-agent transcript with the same name deeper down: not matched
sd=os.path.join(d,SID,"subagents"); os.makedirs(sd,exist_ok=True)
open(os.path.join(sd,"agent-a1.jsonl"),"w").write(line({"type":"user","sessionId":SID,"isSidechain":True,"message":{"role":"user","content":"sub"}})+"\n")

# legacy transcript: no api-request lines
LID="22222222-2222-4333-8444-555555555555"
L=[]
add({"type":"user","isSidechain":False,"message":{"role":"user","content":"hello"}},LID)
add({"type":"assistant","isSidechain":False,"message":{"id":"m1","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"Hi"}],"usage":{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":2}}},LID)
add({"type":"assistant","isSidechain":False,"message":{"id":"m1","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":" there"}],"usage":{"input_tokens":10,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":3}}},LID)
d2=os.path.join(root,"claude","projects","-tmp-legacy"); os.makedirs(d2,exist_ok=True)
open(os.path.join(d2,LID+".jsonl"),"w").write("\n".join(L)+"\n")

# Codex rollout (newer format)
CID="01a1e7ba-0000-7000-8000-000000000001"
def c(t,p,ts="2026-11-17T10:00:00.000Z"): return line({"timestamp":ts,"type":t,"payload":p})
C=[c("session_meta",{"id":CID,"cwd":"/tmp/proj","cli_version":"0.160.0","originator":"codex_cli_rs","base_instructions":{"text":"You are Codex, a coding agent.\nBe concise."},"instructions":"# AGENTS.md\nUse tabs."}),
   c("response_item",{"type":"message","role":"developer","content":[{"type":"input_text","text":"<permissions>sandbox</permissions>"}]}),
   c("response_item",{"type":"message","role":"user","content":[{"type":"input_text","text":"<user_instructions># AGENTS.md\nUse tabs.</user_instructions>"}]}),
   c("response_item",{"type":"message","role":"user","content":[{"type":"input_text","text":"list files"}]}),
   c("turn_context",{"cwd":"/tmp/proj","model":"gpt-5.1-codex","effort":"high"}),
   c("response_item",{"type":"reasoning","summary":[{"type":"summary_text","text":"Listing."}],"encrypted_content":"xxx"}),
   c("response_item",{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\"]}","call_id":"call_1"}),
   c("response_item",{"type":"function_call_output","call_id":"call_1","output":"{\"output\":\"a.go\\nb.go\",\"metadata\":{\"exit_code\":0}}"}),
   c("event_msg",{"type":"token_count","info":{"last_token_usage":{"input_tokens":9000,"cached_input_tokens":8000,"output_tokens":50}}}),
   c("compacted",{"message":"","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"Summary: listed files a.go and b.go."}]}]}),
   c("response_item",{"type":"message","role":"user","content":[{"type":"input_text","text":"now delete b.go"}]}),
   c("response_item",{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin Patch\n*** Delete File: b.go\n*** End Patch","call_id":"call_2"}),
   c("response_item",{"type":"custom_tool_call_output","call_id":"call_2","output":"Done!"}),
   c("response_item",{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Deleted b.go."}]}),
   c("response_item",{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"mcp__crewbase__","description":"","tools":[{"type":"function","name":"computer_screenshot","description":"Take a screenshot.","parameters":{"type":"object","properties":{}}}]}]}),
   c("event_msg",{"type":"token_count","info":None,"rate_limits":{}}),
   c("event_msg",{"type":"token_count","info":{"last_token_usage":{"input_tokens":12000,"cached_input_tokens":11000,"cache_write_input_tokens":0,"output_tokens":30}}},"2026-11-17T10:00:05.000Z"),
]
d3=os.path.join(root,"codex","sessions","2026","11","17"); os.makedirs(d3,exist_ok=True)
open(os.path.join(d3,"rollout-2026-11-17T10-00-00-"+CID+".jsonl"),"w").write("\n".join(C)+"\n")
# older format, archived: instructions only, no base instructions
OID="01a1e7ba-0000-7000-8000-000000000002"
C=[c("session_meta",{"id":OID,"cwd":"/tmp/proj","cli_version":"0.0.0","originator":"codex_cli_rs","instructions":"# AGENTS.md"}),
   c("response_item",{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}),
   c("response_item",{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}),
   c("event_msg",{"type":"token_count","info":{"total_token_usage":{"input_tokens":6997},"last_token_usage":{"input_tokens":6997,"cached_input_tokens":5888,"output_tokens":6}}})]
d4=os.path.join(root,"codex","archived_sessions"); os.makedirs(d4,exist_ok=True)
open(os.path.join(d4,"rollout-2026-11-14T19-39-38-"+OID+".jsonl"),"w").write("\n".join(C)+"\n")
print("ok")
