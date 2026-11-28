// 工作流模板: common pipelines to start from (从模板新建). Each is a workflow
// without agents: importing one opens the editor with its steps filled in,
// and the person picks who does each step before creating it. The prompts
// use {{input}} (the trigger's payload), {{prev}} and {{steps.<id>}}.
//
// Webhook ones say where their events come from: 设置 › Webhook delivers a
// provider's verified events to the workflow (its target).

import type { Workflow } from "../../api";
import { getLang } from "../../i18n";

export interface WorkflowTemplate {
  id: string;
  /** what it is for, and what to connect */
  title: string;
  desc: string;
  /** where its events come from (webhook ones) */
  source?: string;
  workflow: Workflow;
}

type Text = { zh: string; en: string };
const pick = (x: Text) => (getLang() === "en" ? x.en : x.zh);

interface Def {
  id: string;
  title: Text;
  desc: Text;
  source?: Text;
  name: Text;
  trigger: string;
  steps: { id: string; name: Text; mode?: "read-only" | "sandbox"; prompt?: Text; approval?: Text }[];
}

const DEFS: Def[] = [
  {
    id: "pr-review",
    title: { zh: "PR 评审", en: "PR review" },
    desc: { zh: "新 PR 来了先评审，按严重程度列出问题；你确认后作为评论发到 PR 上。", en: "Reviews each new PR and lists issues by severity; posts them as a PR comment once you approve." },
    source: { zh: "设置 › Webhook：GitHub 的 pull_request 事件交给它", en: "Settings › Webhook: GitHub pull_request events to it" },
    name: { zh: "PR 评审", en: "PR review" },
    trigger: "webhook",
    steps: [
      {
        id: "review",
        name: { zh: "评审", en: "Review" },
        mode: "read-only",
        prompt: {
          zh: "这是一个 GitHub PR 事件：\n{{input}}\n\n用本地的 gh 读这个 PR（gh pr view、gh pr diff）。按严重程度列出问题：会出错的、会出安全问题的、该补的测试、可以简化的地方。每条写明文件和行号、为什么是问题、建议怎么改。没有问题就直说。",
          en: "A GitHub pull request event:\n{{input}}\n\nRead the PR with the local gh (gh pr view, gh pr diff). List the issues by severity: what will break, security problems, missing tests, what could be simpler. For each, give the file and line, why it is a problem and how to fix it. If there are none, say so.",
        },
      },
      {
        id: "approve",
        name: { zh: "确认后发出", en: "Approve to post" },
        approval: { zh: "把这些评审意见发到 PR 上？\n\n{{steps.review}}", en: "Post this review on the PR?\n\n{{steps.review}}" },
      },
      {
        id: "post",
        name: { zh: "发评论", en: "Post" },
        mode: "sandbox",
        prompt: {
          zh: "把下面的评审意见作为评论发到这个 PR（gh pr review --comment），保持原意，排版成清楚的列表：\n\n{{steps.review}}\n\nPR 事件：{{input}}",
          en: "Post the review below on this PR as a comment (gh pr review --comment), keeping its meaning, as a clear list:\n\n{{steps.review}}\n\nThe PR event: {{input}}",
        },
      },
    ],
  },
  {
    id: "daily-report",
    title: { zh: "日报", en: "Daily report" },
    desc: { zh: "每天傍晚汇总今天的提交、PR 和待办，写成一份简短的日报。", en: "Each evening, sums up today's commits, PRs and open items in a short report." },
    name: { zh: "日报", en: "Daily report" },
    trigger: "daily 18:00",
    steps: [
      {
        id: "gather",
        name: { zh: "收集", en: "Gather" },
        mode: "read-only",
        prompt: {
          zh: "收集今天的工作：工作区里各个仓库今天的提交（git log --since=midnight）、今天新开和合并的 PR、还没处理的 review 请求和 issue（用本地的 gh）。按仓库列出，只写事实，不评论。",
          en: "Gather today's work: today's commits in the repositories in the workspace (git log --since=midnight), PRs opened and merged today, and review requests and issues still waiting (with the local gh). List them by repository; facts only.",
        },
      },
      {
        id: "write",
        name: { zh: "写日报", en: "Write" },
        mode: "read-only",
        prompt: {
          zh: "根据下面的材料写今天的日报：今天完成了什么、进行中的、卡住的和需要谁帮忙的、明天要做的。每部分几条，短句，不要套话。\n\n{{steps.gather}}",
          en: "Write today's report from the material below: what was done, what is in progress, what is blocked and who could help, what comes tomorrow. A few short points each, no filler.\n\n{{steps.gather}}",
        },
      },
    ],
  },
  {
    id: "sentry-triage",
    title: { zh: "Sentry 告警分诊", en: "Sentry alert triage" },
    desc: { zh: "收到告警先判断严重程度和可能原因；需要修时你点头，它在沙箱里改好并跑测试。", en: "Judges each alert's severity and likely cause; when a fix is needed and you approve, it makes one in the sandbox and runs the tests." },
    source: { zh: "设置 › Webhook：Sentry 的 issue 告警交给它", en: "Settings › Webhook: Sentry issue alerts to it" },
    name: { zh: "Sentry 告警分诊", en: "Sentry triage" },
    trigger: "webhook",
    steps: [
      {
        id: "triage",
        name: { zh: "分诊", en: "Triage" },
        mode: "read-only",
        prompt: {
          zh: "这是一条 Sentry 告警：\n{{input}}\n\n判断：严重程度（影响多少用户、是否在持续发生）、出错的位置（结合仓库代码找到对应的文件和函数）、最可能的原因。如果是已知问题或噪声，直接说明、给出理由。最后一行写「需要修复」或「不需要修复」。",
          en: "A Sentry alert:\n{{input}}\n\nJudge its severity (how many users, whether it keeps happening), where it fails (find the file and function in the repository) and the most likely cause. If it is a known issue or noise, say so and why. End with a line saying either \"fix needed\" or \"no fix needed\".",
        },
      },
      {
        id: "approve",
        name: { zh: "确认修复", en: "Approve the fix" },
        approval: { zh: "要按分诊结果开始修复吗？\n\n{{steps.triage}}", en: "Start a fix from this triage?\n\n{{steps.triage}}" },
      },
      {
        id: "fix",
        name: { zh: "修复", en: "Fix" },
        mode: "sandbox",
        prompt: {
          zh: "根据分诊结果修复这个问题：在工作区里改代码、补一个能复现它的测试、跑相关测试。完成后说明改了什么、为什么这样改、测试结果。不要推送。\n\n{{steps.triage}}",
          en: "Fix the problem from the triage: change the code in the workspace, add a test that reproduces it, run the related tests. Then say what you changed, why, and the test results. Do not push.\n\n{{steps.triage}}",
        },
      },
    ],
  },
  {
    id: "linear-to-pr",
    title: { zh: "Linear 工单转 PR", en: "Linear issue to PR" },
    desc: { zh: "工单来了先读懂、写实现计划，再在沙箱里实现并测试；你确认后推送并开 PR。", en: "Reads each issue and plans it, implements and tests it in the sandbox, then pushes and opens a PR once you approve." },
    source: { zh: "设置 › Webhook：Linear 的 Issue 事件交给它", en: "Settings › Webhook: Linear issue events to it" },
    name: { zh: "Linear 工单转 PR", en: "Linear to PR" },
    trigger: "webhook",
    steps: [
      {
        id: "plan",
        name: { zh: "读懂并计划", en: "Plan" },
        mode: "read-only",
        prompt: {
          zh: "这是一个 Linear 工单事件：\n{{input}}\n\n读懂要做什么，在仓库里找到相关的代码，写一份实现计划：改哪些文件、怎么改、怎么测、有哪些不确定的地方。需求不清楚时列出要问的问题。",
          en: "A Linear issue event:\n{{input}}\n\nWork out what is asked, find the related code in the repository and write an implementation plan: which files change and how, how to test it, what is uncertain. If the request is unclear, list the questions to ask.",
        },
      },
      {
        id: "build",
        name: { zh: "实现", en: "Implement" },
        mode: "sandbox",
        prompt: {
          zh: "按计划在新分支上实现，补上测试并跑通。完成后列出改动的文件和测试结果。\n\n{{steps.plan}}",
          en: "Implement the plan on a new branch, add tests and get them passing. Then list the changed files and the test results.\n\n{{steps.plan}}",
        },
      },
      {
        id: "approve",
        name: { zh: "确认推送", en: "Approve the push" },
        approval: { zh: "推送这个分支并开 PR？\n\n{{steps.build}}", en: "Push this branch and open a PR?\n\n{{steps.build}}" },
      },
      {
        id: "pr",
        name: { zh: "开 PR", en: "Open the PR" },
        mode: "sandbox",
        prompt: {
          zh: "推送分支，用 gh pr create 开 PR：标题写清楚改了什么，描述里引用工单、写明改动和测试方式。\n\n工单：{{input}}\n\n{{steps.build}}",
          en: "Push the branch and open a PR with gh pr create: a title that says what changed, and a description that links the issue and says what changed and how it was tested.\n\nThe issue: {{input}}\n\n{{steps.build}}",
        },
      },
    ],
  },
];

export function workflowTemplates(): WorkflowTemplate[] {
  return DEFS.map((d) => ({
    id: d.id,
    title: pick(d.title),
    desc: pick(d.desc),
    source: d.source && pick(d.source),
    workflow: {
      id: "",
      name: pick(d.name),
      description: [pick(d.desc), d.source && pick(d.source)].filter(Boolean).join(" "),
      trigger: d.trigger,
      steps: d.steps.map((s) =>
        s.approval
          ? { id: s.id, name: pick(s.name), kind: "approval", approval: { summary: pick(s.approval) } }
          : { id: s.id, name: pick(s.name), kind: "agent", agent: "", mode: s.mode, prompt: s.prompt ? pick(s.prompt) : "" },
      ),
    },
  }));
}
