import { getLang } from "./i18n";
import { apiFetch } from "./tunnel";
import { type AddonUpdates, type AgentDocsStatus, type BoardView, type DeviceView, type DocPageCard, type Followup, type ImGateway, type ImPlatform, type MemberView, type PairingCode, type ProxyCheck, type ProxyFound, type ProxyInput, type ProxyView, type RelayStatus, type TaskChange, type TaskView, type WhDelivery, type WhEndpoint, type WhEndpointInput, type WhSettings, type WhSubscription, type WhSubscriptionInput, type Working } from "./api/admin";
import { ApiError, api, enc, getToken, tq, unauthorized } from "./api/client";
import { gq, type GitCommentInput, type GitCommit, type GitFileDiff, type GitLock, type GitMergePreview, type GitPRSummary, type GitPRView, type GitRepoInfo, type GitRepoView, type GitReview, type GitSettings, type GitWorktree } from "./api/git";
import { libQS, type LibFileLink, type LibItem, type LibLink, type LibQuery, type LibStats, type LibVersion } from "./api/library";
import { type AgentView, type ApprovalHistoryItem, type ApprovalRequest, type Attachment, type AutoDraft, type Brief, type BrowserInput, type BrowserState, type ChangesView, type ChannelView, type ComputerOverview, type ComputerPerm, type ComputerState, type ConnectorInfo, type ContextBreakdown, type ContextItemContent, type FileContent, type FileDiff, type FileEntry, type HostView, type ImportOptions, type ImportSource, type Input, type MarketEntry, type McpServer, type McpSkill, type McpStatus, type McpTool, type ModelInfo, type ModelKind, type ModelRef, type ModelSwitches, type ModelsResponse, type NotifyState, type OpenArtifactStatus, type Permissions, type PluginView, type PreviewInfo, type PromptInfo, type Quote, type RememberedChoice, type RoleTemplate, type Routine, type RoutineRun, type Screenshot, type SearchHit, type SessionEvent, type SessionMeta, type SessionSummary, type Settings, type SkillDraft, type SlashPick, type SystemInfo, type TaskRoute, type TeamRole, type TeamView, type TeamVisibility, type TurnChanges, type TurnSummary, type UIContribution, type UIPanel, type UpdateInfo, type UsageStats, type VaultItem, type WfRun, type Workflow } from "./api/types";
// REST helpers and API types (mirrors features/api, core/session, core/agent,
// capabilities/approval). The page is served by crewd on the same origin.

//
// The module is split by area under ./api/ and re-exported here, so every
// import of "../api" keeps working: types.ts (shared shapes), client.ts
// (token and requests), library.ts, git.ts, admin.ts; the API object and
// the providers, arena and memory shapes stay in this file.

export { DEFAULT_TEAM, desktop, openExternal, teamOf, type AgentView, type ApprovalHistoryItem, type ApprovalRequest, type ArtifactCardInfo, type Attachment, type AutoDraft, type Brief, type BrowserInput, type BrowserState, type ChangesView, type ChannelView, type ComputerApp, type ComputerBlocked, type ComputerLive, type ComputerOverview, type ComputerPerm, type ComputerState, type ConnectorInfo, type ContextBreakdown, type ContextCache, type ContextItem, type ContextItemContent, type ContextItemPart, type ContextSegment, type CrewbaseDesktop, type DesktopPermission, type DesktopUpdateState, type DiffLine, type ElicitField, type ElicitSchema, type Elicitation, type FileChange, type FileContent, type FileDiff, type FileEntry, type HostView, type Hunk, type ImportOptions, type ImportSource, type Input, type LastLine, type MarketEntry, type McpServer, type McpSkill, type McpStatus, type McpTool, type McpToolFilter, type Message, type Mode, type ModelInfo, type ModelKind, type ModelPricing, type ModelRef, type ModelState, type ModelSwitches, type ModelUse, type ModelsResponse, type NotifyPrefs, type NotifyState, type OpenArtifactStatus, type PageShown, type Part, type Permissions, type PluginView, type PreviewInfo, type PromptInfo, type ProviderView, type QuietHours, type Quota, type QuotaAccount, type QuotaAccountWindow, type Quote, type RememberedChoice, type RoleTemplate, type Routine, type RoutineRun, type Screenshot, type SearchHit, type SessionEvent, type SessionMeta, type SessionSummary, type Settings, type SkillDraft, type SlashPick, type Status, type StreamChunk, type SystemInfo, type TaskRoute, type TeamRole, type TeamView, type TeamVisibility, type ToolCall, type ToolResult, type TurnChanges, type TurnSummary, type UIContribution, type UIField, type UIPanel, type UpdateInfo, type UsageStats, type VaultItem, type WfRun, type WfStep, type WfStepView, type Workflow } from "./api/types";
export { ApiError, api, getToken, initToken, rawURL, setToken, setUnauthorizedHandler, uploadFile, useRawURL } from "./api/client";
export { libRawText, libRawURL, uploadLibrary, useLibRawURL, type LibFileLink, type LibFolder, type LibItem, type LibLink, type LibQuery, type LibSource, type LibStats, type LibVersion } from "./api/library";
export { type GitCheck, type GitComment, type GitCommentInput, type GitCommit, type GitFileDiff, type GitFileStatus, type GitHunk, type GitLine, type GitLock, type GitMergePreview, type GitOrigin, type GitPRLink, type GitPRSummary, type GitPRView, type GitRepoInfo, type GitRepoView, type GitReview, type GitReviewState, type GitSettings, type GitSeverity, type GitStatus, type GitVerdict, type GitWorktree } from "./api/git";
export { type AddonUpdate, type AddonUpdates, type AgentDocsStatus, type BoardView, type DeviceView, type DocPageCard, type Followup, type ImGateway, type ImPlatform, type ImRegion, type MemberView, type PairingCode, type ProxyCheck, type ProxyFound, type ProxyInput, type ProxySettings, type ProxyType, type ProxyView, type RelayStatus, type TaskChange, type TaskView, type ToldEntry, type WhDelivery, type WhEndpoint, type WhEndpointInput, type WhSettings, type WhSubscription, type WhSubscriptionInput, type WhTarget, type WhTargetKind, type WorkNote, type WorkTask, type Working } from "./api/admin";


export const API = {
  steer: (agent: string, input: string) => api<AgentView>("POST", `/api/agents/${enc(agent)}/queue/${enc(input)}/steer`),
  addonUpdates: () => api<AddonUpdates>("GET", "/api/addons/updates"),
  checkAddonUpdates: () => api<AddonUpdates>("POST", "/api/addons/updates/check"),
  applyAddonUpdate: (b: { kind: string; id: string; agent?: string }) => api<{ result: any; state: AddonUpdates }>("POST", "/api/addons/update", b),
  agentDocs: () => api<AgentDocsStatus>("GET", "/api/agentdocs"),
  /** a new admin token: the docs password (a server Crewbase runs) or a pasted one */
  repairAgentDocs: (b: { password?: string; token?: string }) => api<AgentDocsStatus>("POST", "/api/agentdocs/repair", b),
  agentDocsPage: (url: string) => api<DocPageCard>("GET", `/api/agentdocs/page?url=${encodeURIComponent(url)}&blocks=12`),
  /** signs this browser in to the docs when Crewbase holds the session */
  embedAgentDocs: () => api<{ url: string; signedIn: boolean }>("POST", "/api/agentdocs/embed"),
  connectAgentDocs: (b: { url: string; token: string; workspace?: string; agents?: string[]; wake?: boolean }) => api<AgentDocsStatus>("POST", "/api/agentdocs", b),
  patchAgentDocs: (b: { agents?: string[]; wake?: boolean }) => api<AgentDocsStatus>("PATCH", "/api/agentdocs", b),
  syncAgentDocs: () => api<AgentDocsStatus>("POST", "/api/agentdocs/sync"),
  startAgentDocs: (b: { email: string; password: string }) => api<AgentDocsStatus>("POST", "/api/agentdocs/local", b),
  disconnectAgentDocs: () => api<AgentDocsStatus>("DELETE", "/api/agentdocs"),
  library: (team: string, q: LibQuery = {}) => api<{ items: LibItem[]; stats?: LibStats }>("GET", `/api/library?${libQS(team, q as Record<string, string | number | undefined>)}`),
  libraryItem: (team: string, id: number) => api<{ item: LibItem; versions: LibVersion[] }>("GET", `/api/library/${id}?${libQS(team)}`),
  librarySettings: () => api<{ summaries: boolean }>("GET", "/api/library/settings"),
  setLibrarySettings: (b: { summaries: boolean }) => api<{ summaries: boolean }>("PUT", "/api/library/settings", b),
  libraryText: (team: string, id: number) => api<{ text: string }>("GET", `/api/library/${id}/text?${libQS(team)}`),
  patchLibrary: (team: string, id: number, b: { title?: string; tags?: string[]; private?: boolean }) => api<LibItem>("PATCH", `/api/library/${id}?${libQS(team)}`, b),
  deleteLibrary: (team: string, id: number) => api("DELETE", `/api/library/${id}?${libQS(team)}`),
  restoreLibrary: (team: string, id: number, version: number) => api<LibItem>("POST", `/api/library/${id}/restore?${libQS(team)}`, { version }),
  gitRepo: (agent: string, all = false) => api<GitRepoView>("GET", `/api/git/repo?${gq({ agent, all: all ? "1" : undefined })}`),
  gitInit: (agent: string) => api<GitRepoInfo>("POST", "/api/git/init", { agent }),
  gitDiff: (agent: string, o: { worktree?: string; side?: string; base?: string; path?: string }) => api<{ files: GitFileDiff[] }>("GET", `/api/git/diff?${gq({ agent, ...o })}`),
  gitLog: (agent: string, worktree?: string, rev?: string) => api<{ commits: GitCommit[] }>("GET", `/api/git/log?${gq({ agent, worktree, rev })}`),
  gitHunk: (b: { agent: string; worktree?: string; path: string; hunk: string; action: "stage" | "unstage" | "discard" }) => api("POST", "/api/git/hunk", b),
  gitFiles: (b: { agent: string; worktree?: string; paths: string[]; action: "stage" | "unstage" | "discard" }) => api("POST", "/api/git/files", b),
  gitCommit: (b: { agent: string; worktree?: string; message: string; all: boolean }) => api<GitCommit>("POST", "/api/git/commit", b),
  gitPush: (b: { agent: string; worktree?: string; branch?: string; remote?: string; force?: boolean }) => api<{ output: string }>("POST", "/api/git/push", b),
  gitSpawn: (b: { agent: string; base?: string; branch?: string; task?: string }) => api<GitWorktree>("POST", "/api/git/worktrees", b),
  gitRemoveWorktree: (agent: string, id: string, discard = false) => api<GitWorktree>("DELETE", `/api/git/worktrees/${id}?${gq({ agent, discard: discard ? "1" : undefined })}`),
  gitOpenReview: (b: { agent: string; worktree?: string; source?: string; target?: string; title?: string; body?: string; reviewers?: string[]; draft?: boolean }) =>
    api<GitReview>("POST", "/api/git/reviews", b),
  gitReview: (agent: string, id: string) =>
    api<{ review: GitReview; files: GitFileDiff[]; preview?: GitMergePreview; me: string; selfReview: boolean }>("GET", `/api/git/reviews/${id}?${gq({ agent })}`),
  gitSubmit: (agent: string, id: string, b: { decision: "approve" | "request_changes" | "comment"; body?: string; comments?: GitCommentInput[] }) =>
    api<GitReview>("POST", `/api/git/reviews/${id}/submit`, { agent, ...b }),
  gitResolveComment: (agent: string, id: string, cid: string, resolved: boolean) => api<GitReview>("POST", `/api/git/reviews/${id}/comments/${cid}/resolve`, { agent, resolved }),
  gitReviewAct: (agent: string, id: string, act: "publish" | "close" | "again") => api<GitReview>("POST", `/api/git/reviews/${id}/${act}`, { agent }),
  gitMerge: (agent: string, id: string, strategy: string) => api<{ review: GitReview; commit: GitCommit }>("POST", `/api/git/reviews/${id}/merge`, { agent, strategy }),
  gitLock: (b: { agent: string; path: string; start?: number; end?: number; reason?: string }) => api<GitLock>("POST", "/api/git/locks", b),
  gitUnlock: (agent: string, id: string) => api("DELETE", `/api/git/locks/${id}?${gq({ agent })}`),
  gitPRs: (agent: string, state?: string) => api<{ prs: GitPRSummary[] }>("GET", `/api/git/prs?${gq({ agent, state })}`),
  gitPR: (agent: string, n: number) => api<GitPRView>("GET", `/api/git/prs/${n}?${gq({ agent })}`),
  gitPRCheckout: (agent: string, n: number, holder?: string) => api<GitWorktree>("POST", `/api/git/prs/${n}/checkout`, { agent, holder }),
  gitPRCreate: (agent: string, review: string, b: { title?: string; body?: string; draft?: boolean }) => api<GitReview>("POST", "/api/git/prs", { agent, review, ...b }),
  gitPRReview: (agent: string, review: string, n: number) => api<{ review: GitReview; event: string }>("POST", `/api/git/prs/${n}/review`, { agent, review }),
  gitPRSync: (agent: string, review: string, n: number) => api<GitReview>("POST", `/api/git/prs/${n}/sync`, { agent, review }),
  /** approve: the person's 合并 approves the review's head in the same step */
  gitPRMerge: (agent: string, review: string, n: number, method: string, approve = false) =>
    api<{ review: GitReview; commit: string }>("POST", `/api/git/prs/${n}/merge`, { agent, review, method, approve: approve || undefined }),
  gitSettings: () => api<GitSettings>("GET", "/api/git/settings"),
  setGitSettings: (b: GitSettings) => api<GitSettings>("PUT", "/api/git/settings", b),
  webhooks: (team: string) => api<{ endpoints: WhEndpoint[]; subscriptions: WhSubscription[]; providers: string[]; events: string[] }>("GET", `/api/webhooks?${tq(team)}`),
  saveEndpoint: (team: string, id: string | null, b: WhEndpointInput) =>
    api<{ endpoint: WhEndpoint; secret: string }>(id ? "PATCH" : "POST", `/api/webhooks/endpoints${id ? "/" + id : ""}?${tq(team)}`, b),
  deleteEndpoint: (team: string, id: string) => api("DELETE", `/api/webhooks/endpoints/${id}?${tq(team)}`),
  rotateRelay: (team: string, id: string) => api<WhEndpoint>("POST", `/api/webhooks/endpoints/${id}/relay-token?${tq(team)}`),
  saveSubscription: (team: string, id: string | null, b: WhSubscriptionInput) =>
    api<{ subscription: WhSubscription; secret: string }>(id ? "PATCH" : "POST", `/api/webhooks/subscriptions${id ? "/" + id : ""}?${tq(team)}`, b),
  deleteSubscription: (team: string, id: string) => api("DELETE", `/api/webhooks/subscriptions/${id}?${tq(team)}`),
  testSubscription: (team: string, id: string) => api<WhDelivery>("POST", `/api/webhooks/subscriptions/${id}/test?${tq(team)}`),
  deliveries: (team: string, of?: string, n?: number) => api<{ deliveries: WhDelivery[] }>("GET", `/api/webhooks/deliveries?${tq(team, { of, n })}`),
  delivery: (team: string, id: string) => api<WhDelivery>("GET", `/api/webhooks/deliveries/${id}?${tq(team)}`),
  replayDelivery: (team: string, id: string, dry: boolean) => api<WhDelivery>("POST", `/api/webhooks/deliveries/${id}/replay?${tq(team)}`, { dry }),
  webhookSettings: () => api<WhSettings>("GET", "/api/webhooks/settings"),
  setWebhookSettings: (b: WhSettings) => api<WhSettings>("PUT", "/api/webhooks/settings", b),
  proxySettings: () => api<ProxyView>("GET", "/api/settings/proxy"),
  setProxy: (b: ProxyInput) => api<ProxyView>("PUT", "/api/settings/proxy", b),
  testProxy: (b: ProxyInput) => api<ProxyCheck>("POST", "/api/settings/proxy/test", b),
  detectProxy: () => api<{ found: ProxyFound[] }>("GET", "/api/settings/proxy/detect"),
  publishToLibrary: (team: string, b: { agent: string; path: string; session?: string; seq?: number; title?: string }) =>
    api<{ link: LibLink; item: LibItem }>("POST", `/api/library/publish?${libQS(team)}`, b),
  libraryLinks: (team: string, agent: string) => api<{ links: LibFileLink[] }>("GET", `/api/library/links?${libQS(team, { agent })}`),
  libraryLink: (team: string, id: number) => api<{ link: LibLink | null }>("GET", `/api/library/${id}/link?${libQS(team)}`),
  syncLibrary: (team: string, id: number) => api<{ link: LibLink; item: LibItem }>("POST", `/api/library/${id}/sync?${libQS(team)}`),
  pullLibrary: (team: string, id: number) => api<{ link: LibLink }>("POST", `/api/library/${id}/pull?${libQS(team)}`),
  unlinkLibrary: (team: string, id: number) => api("DELETE", `/api/library/${id}/link?${libQS(team)}`),
  attachLibrary: (team: string, id: number, agent: string) => api<Attachment>("POST", `/api/library/${id}/attach?${libQS(team)}`, { agent }),
  unfurl: (url: string) => api<LinkCard>("GET", `/api/unfurl?url=${enc(url)}`),
  updates: () => api<UpdateInfo>("GET", "/api/updates"),
  checkUpdates: () => api<UpdateInfo>("POST", "/api/updates/check"),
  teams: () => api<{ teams: TeamView[] }>("GET", "/api/teams"),
  createTeam: (b: { name: string; visibility: TeamVisibility; color?: string }) => api<TeamView>("POST", "/api/teams", b),
  teamMemory: (id: string) => api<{ text: string }>("GET", `/api/teams/${enc(id)}/memory`),
  setTeamMemory: (id: string, text: string) => api<{ text: string }>("PUT", `/api/teams/${enc(id)}/memory`, { text }),
  patchTeam: (id: string, b: { name?: string; visibility?: TeamVisibility; color?: string; people?: { id: string; role: TeamRole }[]; defaultMode?: string }) =>
    api<TeamView>("PATCH", `/api/teams/${enc(id)}`, b),
  joinTeam: (id: string) => api<TeamView>("POST", `/api/teams/${enc(id)}/join`),
  deleteTeam: (id: string) => api("DELETE", `/api/teams/${enc(id)}`),
  moveToTeam: (team: string, agent: string) => api<{ id: string; team: string }>("POST", `/api/teams/${enc(team)}/agents`, { agent }),
  setProjectDir: (agent: string, dir: string) => api<AgentView>("PATCH", `/api/agents/${enc(agent)}`, { projectDir: dir }),
  followups: (agent: string) => api<{ followups: Followup[] }>("GET", `/api/agents/${enc(agent)}/followups`),
  cancelFollowup: (agent: string, id: string) => api("DELETE", `/api/agents/${enc(agent)}/followups/${enc(id)}`),
  browseDirs: (path?: string) => api<{ path: string; parent: string; home: string; dirs: { name: string; path: string }[] }>("GET", `/api/fs/dirs?path=${enc(path ?? "")}`),
  working: (agent: string) => api<Working>("GET", `/api/agents/${enc(agent)}/working`),
  taskProgress: (agent: string, name: string) => api<{ name: string; text: string }>("GET", `/api/agents/${enc(agent)}/tasks/${enc(name)}`),
  note: (agent: string, name: string) => api<{ name: string; text: string }>("GET", `/api/agents/${enc(agent)}/notes/${enc(name)}`),
  putNote: (agent: string, name: string, text: string) => api<{ name: string }>("PUT", `/api/agents/${enc(agent)}/notes/${enc(name)}`, { text }),
  deleteNote: (agent: string, name: string) => api("DELETE", `/api/agents/${enc(agent)}/notes/${enc(name)}`),
  imConfig: () => api<{ platforms: ImPlatform[]; gateways: ImGateway[] }>("GET", "/api/im/config"),
  saveIm: (id: string, b: Record<string, unknown>) => api<ImGateway>("PUT", `/api/im/config/${enc(id)}`, b),
  removeIm: (id: string) => api("DELETE", `/api/im/config/${enc(id)}`),
  testIm: (id: string) => api<{ ok: boolean; error?: string }>("POST", `/api/im/${enc(id)}/test`),
  verifyIm: (b: { platform?: string; region?: string; appId?: string; botToken?: string; appToken?: string; id?: string }) =>
    api<{ ok: boolean; bot?: string; error?: string }>("POST", "/api/im/config/verify", b),
  imChats: (b: { platform?: string; region?: string; appId?: string; botToken?: string; id?: string }) =>
    api<{ chats: { id: string; name: string; description?: string; external?: boolean }[]; hint?: "permission" | "empty" | "unsupported" }>("POST", "/api/im/config/chats", b),
  health: () => api<{ ok: boolean }>("GET", "/api/health"),
  pair: (b: { code: string; name: string; platform: string }) => api<{ token: string; device: DeviceView }>("POST", "/api/pair", b),
  whoami: () => api<{ tier: string; device?: DeviceView }>("GET", "/api/whoami"),
  me: () => api<{ id: string; name: string; role: "owner" | "admin" | "member" | "viewer"; approve: string[] }>("GET", "/api/me"),
  members: () => api<{ members: MemberView[]; ownerDevices: number; roles: string[]; approvable: string[] }>("GET", "/api/members"),
  addTeamMember: (b: { name: string; role: string }) => api<MemberView>("POST", "/api/members", b),
  updateMember: (id: string, b: { name?: string; role?: string; approve?: string[] }) => api<MemberView>("PATCH", `/api/members/${enc(id)}`, b),
  removeMember: (id: string) => api<{ devices: number }>("DELETE", `/api/members/${enc(id)}`),
  board: (session: string) => api<BoardView>("GET", `/api/board?session=${encodeURIComponent(session)}`),
  addTask: (session: string, b: TaskChange) => api<TaskView>("POST", `/api/board/tasks?session=${encodeURIComponent(session)}`, b),
  updateTask: (session: string, id: string, b: TaskChange) => api<TaskView>("PATCH", `/api/board/tasks/${enc(id)}?session=${encodeURIComponent(session)}`, b),
  removeTask: (session: string, id: string) => api("DELETE", `/api/board/tasks/${enc(id)}?session=${encodeURIComponent(session)}`),
  devices: () => api<{ devices: DeviceView[]; pending: PairingCode[] }>("GET", "/api/devices"),
  startPairing: (tier: string, member = "") => api<PairingCode>("POST", "/api/devices/pairing", { tier, member }),
  cancelPairing: (code: string) => api("DELETE", `/api/devices/pairing/${enc(code)}`),
  renameDevice: (id: string, name: string) => api<DeviceView>("PATCH", `/api/devices/${enc(id)}`, { name }),
  revokeDevice: (id: string) => api("DELETE", `/api/devices/${enc(id)}`),
  relay: () => api<RelayStatus>("GET", "/api/relay"),
  relayPairLink: (code: string) => api<{ url: string }>("POST", "/api/relay/pair-link", { code }),
  agents: () => api<{ agents: AgentView[] }>("GET", "/api/agents"),
  agent: (id: string) => api<AgentView>("GET", `/api/agents/${enc(id)}`),
  createAgent: (b: Partial<AgentView> & { modelRefs?: string[]; template?: string; greet?: boolean }) => api<AgentView>("POST", "/api/agents", b),
  ui: () => api<{ slots: string[]; contributions: UIContribution[] }>("GET", "/api/ui"),
  uiPanel: (slot: string, key: string, session = "") => api<UIPanel>("POST", "/api/ui/panel", { slot, key, session, lang: getLang() }),
  uiRun: (slot: string, key: string, session = "") => api<{ toast?: string; open?: string }>("POST", "/api/ui/run", { slot, key, session, lang: getLang() }),
  previewImport: (src: ImportSource) => api<RoleTemplate>("POST", "/api/agents/import/preview", src),
  importAgent: (src: ImportSource, options: ImportOptions) =>
    api<{ agent: string; session: string; name: string; skills: number }>("POST", "/api/agents/import", { ...src, options }),
  patchAgent: (id: string, b: Record<string, unknown>) => api<AgentView>("PATCH", `/api/agents/${enc(id)}`, b),
  deleteAgent: (id: string) => api("DELETE", `/api/agents/${enc(id)}`),
  send: (id: string, b: { text: string; model?: string; session?: string; front?: boolean; queue?: boolean; attachments?: { path: string; name: string }[]; reply?: Quote; use?: SlashPick[] }) =>
    api<Input>("POST", `/api/agents/${enc(id)}/messages`, b),
  /** Regenerate (no text: resend seq and the messages after it as they
   *  were) or edit and resend (text: only seq, edited). History from seq on
   *  no longer reaches the agent's model; the log keeps it. */
  resend: (id: string, b: { session: string; seq: number; text?: string; model?: string }) =>
    api<{ rewind: number; from: number; reason: string; inputs: Input[] }>("POST", `/api/agents/${enc(id)}/resend`, b),
  action: (id: string, a: "stop" | "pause" | "resume") => api<AgentView>("POST", `/api/agents/${enc(id)}/${a}`),
  moveInput: (id: string, input: string, by: number) => api<AgentView>("POST", `/api/agents/${enc(id)}/queue/${enc(input)}/move`, { by }),
  cancelInput: (id: string, input: string) => api<AgentView>("DELETE", `/api/agents/${enc(id)}/queue/${enc(input)}`),
  sessions: () => api<{ sessions: SessionMeta[] }>("GET", "/api/sessions"),
  events: (id: string, since: number) =>
    api<{ meta: SessionMeta; events: SessionEvent[] }>("GET", `/api/sessions/${enc(id)}/events?since=${since}`),
  approvals: () => api<{ pending: ApprovalRequest[] }>("GET", "/api/approvals"),
  decide: (callId: string, b: { outcome: "allowed-once" | "rejected" | "cancelled"; message?: string; remember?: boolean; confirmed?: boolean; values?: Record<string, unknown> }) =>
    api("POST", `/api/approvals/${enc(callId)}`, b),
  models: () => api<ModelsResponse>("GET", "/api/models"),

  summaries: () => api<{ sessions: SessionSummary[] }>("GET", "/api/sessions/summary"),
  context: (id: string, session?: string) =>
    api<ContextBreakdown>("GET", `/api/agents/${enc(id)}/context${session ? "?session=" + enc(session) : ""}`),
  contextItem: (id: string, item: string, session?: string) =>
    api<ContextItemContent>("GET", `/api/agents/${enc(id)}/context/item?id=${enc(item)}${session ? "&session=" + enc(session) : ""}`),
  compact: (id: string, session?: string) => api("POST", `/api/agents/${enc(id)}/compact`, { session }),
  newConversation: (id: string) => api<SessionMeta>("POST", `/api/agents/${enc(id)}/sessions`, {}),
  files: (id: string, path: string) => api<{ entries: FileEntry[] }>("GET", `/api/agents/${enc(id)}/files?path=${enc(path)}`),
  file: (id: string, path: string) => api<FileContent>("GET", `/api/agents/${enc(id)}/file?path=${enc(path)}`),
  changes: (id: string) => api<ChangesView>("GET", `/api/agents/${enc(id)}/changes`),
  fileDiff: (id: string, path: string) => api<FileDiff>("GET", `/api/agents/${enc(id)}/changes/diff?path=${enc(path)}`),
  revertChange: (id: string, path: string) => api<ChangesView>("POST", `/api/agents/${enc(id)}/changes/revert`, { path }),
  turnSummaries: (id: string) => api<TurnSummary[]>("GET", `/api/agents/${enc(id)}/turns`),
  turnChanges: (id: string, turn: string) => api<TurnChanges>("GET", `/api/agents/${enc(id)}/turns/${enc(turn)}`),
  turnDiff: (id: string, turn: string, path: string) => api<FileDiff>("GET", `/api/agents/${enc(id)}/turns/${enc(turn)}/diff?path=${enc(path)}`),
  restoreTurn: (id: string, turn: string) => api<{ restored: number }>("POST", `/api/agents/${enc(id)}/turns/${enc(turn)}/restore`),
  acceptChanges: (id: string) => api<ChangesView>("POST", `/api/agents/${enc(id)}/changes/accept`),
  saveFile: (id: string, path: string, content: string) => api("PUT", `/api/agents/${enc(id)}/file`, { path, content }),
  // 预览 (features/preview)
  openPreview: (id: string, path: string, session?: string, title?: string) => api<PreviewInfo>("POST", `/api/agents/${enc(id)}/preview`, { path, session, title }),
  previewHTML: (id: string, html: string, session?: string, title?: string) => api<PreviewInfo>("POST", `/api/agents/${enc(id)}/preview/html`, { html, session, title }),
  previews: (session: string) => api<{ previews: PreviewInfo[] }>("GET", `/api/sessions/${enc(session)}/previews`),

  remembered: () => api<{ remembered: RememberedChoice[] }>("GET", "/api/approvals/remembered"),
  forget: (agent: string, category: string) => api("DELETE", `/api/approvals/remembered/${enc(agent)}/${enc(category)}`),
  approvalHistory: (limit = 100) => api<{ history: ApprovalHistoryItem[] }>("GET", `/api/approvals/history?limit=${limit}`),

  resetModel: (model: string) => api("POST", "/api/models/reset", { model }),
  routes: () => api<{ routes: TaskRoute[]; tasks: string[]; inUse?: Record<string, string> }>("GET", "/api/router/routes"),
  setRoutes: (routes: TaskRoute[]) => api<{ routes: TaskRoute[] }>("PUT", "/api/router/routes", { routes }),
  settings: () => api<Settings>("GET", "/api/settings"),
  patchSettings: (b: Partial<Settings>) => api<Settings>("PATCH", "/api/settings", b),
  notify: () => api<NotifyState>("GET", "/api/notify"),
  openArtifact: () => api<OpenArtifactStatus>("GET", "/api/open-artifact"),
  openArtifactLatest: () => api<{ version: string }>("GET", "/api/open-artifact/latest"),
  installOpenArtifact: (version?: string) => api<OpenArtifactStatus>("POST", "/api/open-artifact/install", { version: version ?? "" }),
  setOpenArtifact: (enabled: boolean) => api<OpenArtifactStatus>("POST", "/api/open-artifact/enabled", { enabled }),
  computer: () => api<ComputerOverview>("GET", "/api/computer"),
  setComputer: (enabled: boolean) => api<ComputerOverview>("PATCH", "/api/computer", { enabled }),
  setComputerPermission: (agent: string, app: string, permission: ComputerPerm) =>
    api<ComputerOverview>("PUT", "/api/computer/permissions", { agent, app, permission }),
  setComputerBlocked: (key: string, blocked: boolean) => api<ComputerOverview>("PUT", "/api/computer/blocked", { key, blocked }),
  stopComputer: (agent?: string) =>
    api<ComputerState>("POST", agent ? `/api/agents/${enc(agent)}/computer/stop` : "/api/computer/stop"),
  resumeComputer: (agent?: string) => api<ComputerState>("POST", "/api/computer/resume", agent ? { agent } : {}),
  testNotify: (channel: string) => api<{ ok: boolean }>("POST", "/api/notify/test", { channel }),
  hosts: () => api<{ hosts: HostView[] }>("GET", "/api/hosts"),

  channels: () => api<{ channels: ChannelView[] }>("GET", "/api/channels"),
  channel: (id: string) => api<ChannelView>("GET", `/api/channels/${enc(id)}`),
  createChannel: (b: { name: string; members: string[]; folder?: string; handoverConfirm?: boolean }) => api<ChannelView>("POST", "/api/channels", b),
  patchChannel: (id: string, b: { handoverConfirm: boolean }) => api<ChannelView>("PATCH", `/api/channels/${enc(id)}`, b),
  postChannel: (id: string, text: string, reply?: Quote, use?: SlashPick[]) =>
    api<{ agent: string; input: string; position: number }>("POST", `/api/channels/${enc(id)}/messages`, { text, reply, use }),
  /** pin a message to the top of its conversation (remove: unpin) */
  pin: (session: string, target: number, remove?: boolean) => api<null>("POST", `/api/sessions/${enc(session)}/pins`, { target, remove: remove || undefined }),
  /** messages containing q in every conversation the caller sees */
  search: (q: string) => api<{ hits: SearchHit[] }>("GET", `/api/search?q=${enc(q)}`),
  /** the person has seen session up to seq (0: all of it) */
  markRead: (session: string, seq: number) => api<{ read: number }>("POST", `/api/sessions/${enc(session)}/read`, { seq }),
  /** an emoji on a message (features/reactions); remove takes yours back */
  react: (session: string, target: number, emoji: string, remove?: boolean) =>
    api<null>("POST", `/api/sessions/${enc(session)}/reactions`, { target, emoji, remove: remove || undefined }),
  providers: () => api<{ providers: UserProvider[]; presets: ProviderPreset[] }>("GET", "/api/providers"),
  testProvider: (b: ProviderInput) => api<ProviderTestResult>("POST", "/api/providers/test", b),
  addProvider: (b: ProviderInput & { models: string[]; kinds?: Record<string, ModelKind> }) => api<UserProvider>("POST", "/api/providers", b),
  removeProvider: (id: string) => api("DELETE", `/api/providers/${enc(id)}`),
  updateProvider: (id: string, b: ProviderPatch) => api<UserProvider>("PATCH", `/api/providers/${enc(id)}`, b),
  modelSwitches: (provider: string) => api<ModelSwitches>("GET", `/api/models/${encodeURIComponent(provider)}/switches`),
  setModelSwitch: (provider: string, model: string, enabled: boolean) =>
    api<ModelSwitches>("PUT", `/api/models/${encodeURIComponent(provider)}/switches`, { model, enabled }),
  magpieModels: () => api<MagpieChoice>("GET", "/api/providers/magpie/models"),
  setMagpieModels: (enabled: string[] | null) => api<MagpieChoice>("PUT", "/api/providers/magpie/models", { enabled }),
  modelCatalog: () => api<CatalogStatus>("GET", "/api/model-catalog"),
  refreshModelCatalog: () => api<CatalogStatus>("POST", "/api/model-catalog/refresh"),
  setModelCatalog: (b: { sources: string[]; autoRefresh: boolean }) => api<CatalogStatus>("PUT", "/api/model-catalog/settings", b),
  updateChannel: (id: string, b: { title?: string; folder?: string; handoverConfirm?: boolean }) => api<ChannelView>("PATCH", `/api/channels/${enc(id)}`, b),
  deleteChannel: (id: string) => api("DELETE", `/api/channels/${enc(id)}`),
  createFolder: (name: string) => api<{ name: string }>("POST", "/api/folders", { name }),
  renameFolder: (name: string, to: string) => api<{ moved: number }>("PATCH", `/api/folders/${enc(name)}`, { name: to }),
  dissolveFolder: (name: string) => api<{ moved: number }>("DELETE", `/api/folders/${enc(name)}`),
  channelAction: (id: string, a: "stop" | "resume" | "archive") => api<ChannelView>("POST", `/api/channels/${enc(id)}/${a}`),
  reassign: (id: string, to: string, note?: string) => api<ChannelView>("POST", `/api/channels/${enc(id)}/reassign`, { to, note }),
  addMember: (id: string, agent: string) => api<ChannelView>("POST", `/api/channels/${enc(id)}/members`, { agent }),

  routines: () => api<{ routines: Routine[] }>("GET", "/api/routines"),
  createRoutine: (b: Partial<Routine>) => api<Routine>("POST", "/api/routines", b),
  patchRoutine: (id: string, b: Partial<Pick<Routine, "name" | "prompt" | "schedule" | "enabled" | "mode">>) =>
    api<Routine>("PATCH", `/api/routines/${enc(id)}`, b),
  deleteRoutine: (id: string) => api("DELETE", `/api/routines/${enc(id)}`),
  runRoutine: (id: string) => api("POST", `/api/routines/${enc(id)}/run`),
  routineRuns: (id: string) => api<{ runs: RoutineRun[] }>("GET", `/api/routines/${enc(id)}/runs`),
  brief: () => api<{ preview: Brief; history: Brief[] }>("GET", "/api/brief"),

  workflows: () => api<{ workflows: Workflow[] }>("GET", "/api/workflows"),
  createWorkflow: (b: Workflow) => api<Workflow>("POST", "/api/workflows", b),
  putWorkflow: (id: string, b: Workflow) => api<Workflow>("PUT", `/api/workflows/${enc(id)}`, b),
  deleteWorkflow: (id: string) => api("DELETE", `/api/workflows/${enc(id)}`),
  runWorkflow: (id: string, input: string) => api<WfRun>("POST", `/api/workflows/${enc(id)}/run`, { input }),
  workflowRuns: (id: string) => api<{ runs: WfRun[] | null }>("GET", `/api/workflows/${enc(id)}/runs`),
  workflowRun: (run: string) => api<WfRun>("GET", `/api/workflow-runs/${enc(run)}`),
  rerun: (run: string, from: string) => api<WfRun>("POST", `/api/workflow-runs/${enc(run)}/rerun`, { from }),
  cancelRun: (run: string) => api("POST", `/api/workflow-runs/${enc(run)}/cancel`),
  skillDraft: (session: string, b: { agent: string; upto: number; scope: "turn" | "all" }) => api<SkillDraft>("POST", `/api/sessions/${enc(session)}/skill-draft`, b),
  /** a skill written in the app: for one agent (its workspace), or every agent */
  saveSkill: (b: { name: string; content: string; agent?: string }) => api<{ name: string; dir: string }>("POST", "/api/skills", b),
  automationDraft: (session: string, b: { agent: string; upto: number; scope: "turn" | "all"; kind?: "routine" | "workflow" }) =>
    api<AutoDraft>("POST", `/api/sessions/${enc(session)}/automation-draft`, b),

  mcp: () => api<{ servers: McpServer[] | null }>("GET", "/api/mcp"),
  mcpSignOut: (name: string) => api("POST", `/api/mcp/${enc(name)}/signout`),
  addMcp: (b: Partial<McpServer>) => api<McpServer>("POST", "/api/mcp", b),
  patchMcp: (name: string, b: Partial<McpServer>) => api<McpServer>("PATCH", `/api/mcp/${enc(name)}`, b),
  deleteMcp: (name: string) => api("DELETE", `/api/mcp/${enc(name)}`),
  probeMcp: (name: string) => api<{ ok: boolean; error?: string; tools: McpTool[] }>("POST", `/api/mcp/${enc(name)}/probe`),
  mcpSkills: (name: string) => api<{ skills: McpSkill[] | null; error?: string }>("GET", `/api/mcp/${enc(name)}/skills`),
  mcpTools: (name: string) => api<{ tools: McpTool[] | null; status: McpStatus }>("GET", `/api/mcp/${enc(name)}/tools`),
  connectors: () => api<{ connectors: ConnectorInfo[] | null }>("GET", "/api/connectors"),

  plugins: () => api<{ plugins: PluginView[] | null }>("GET", "/api/plugins/installed"),
  market: () => api<{ plugins: MarketEntry[] }>("GET", "/api/market"),
  install: (b: { source?: string; market?: string }) => api<PluginView>("POST", "/api/plugins/install", b),
  approvePlugin: (name: string, permissions: Permissions, trustFutureVersions: boolean) =>
    api<PluginView>("POST", `/api/plugins/${enc(name)}/approve`, { permissions, trustFutureVersions }),
  pluginAction: (name: string, a: "enable" | "disable" | "reload") => api<PluginView>("POST", `/api/plugins/${enc(name)}/${a}`),
  deletePlugin: (name: string) => api("DELETE", `/api/plugins/${enc(name)}`),

  browser: (id: string) => api<BrowserState>("GET", `/api/agents/${enc(id)}/browser`),
  takeover: (id: string) => api<BrowserState>("POST", `/api/agents/${enc(id)}/browser/takeover`),
  handback: (id: string) => api<BrowserState>("POST", `/api/agents/${enc(id)}/browser/handback`),
  /** Move a takeover to a desktop window (open) or back to the in-app frame view. */
  browserWindow: (id: string, open: boolean) => api<BrowserState>("POST", `/api/agents/${enc(id)}/browser/window`, { open }),
  screenshot: (id: string) => api<Screenshot>("GET", `/api/agents/${enc(id)}/browser/screenshot`),
  /** One frame as a PNG (an object URL the caller revokes); falls back to JSON on older daemons. */
  screenshotFrame: async (id: string): Promise<{ src: string; width: number; height: number; url: string; revoke: boolean }> => {
    const headers: Record<string, string> = {};
    const tok = getToken();
    if (tok) headers.Authorization = `Bearer ${tok}`;
    const res = await apiFetch(`/api/agents/${enc(id)}/browser/screenshot?raw=1`, { headers });
    const ct = res.headers.get("Content-Type") ?? "";
    if (!res.ok) {
      const data = await res.json().catch(() => null);
      if (res.status === 401) unauthorized();
      throw new ApiError(res.status, data?.error?.code ?? "http_" + res.status, data?.error?.message ?? res.statusText);
    }
    if (ct.startsWith("application/json")) {
      const s = (await res.json()) as Screenshot;
      return { src: `data:${s.mime};base64,${s.data}`, width: s.width ?? 0, height: s.height ?? 0, url: s.url ?? "", revoke: false };
    }
    const src = URL.createObjectURL(await res.blob());
    const url = decodeURIComponent(res.headers.get("X-Crewbase-Url") ?? "");
    return { src, width: Number(res.headers.get("X-Crewbase-Width")) || 0, height: Number(res.headers.get("X-Crewbase-Height")) || 0, url, revoke: true };
  },
  browserInput: (id: string, b: BrowserInput) => api("POST", `/api/agents/${enc(id)}/browser/input`, b),

  usage: () => api<UsageStats>("GET", "/api/usage"),
  workspaceDir: () => api<{ dir: string; isDefault: boolean; default: string }>("GET", "/api/settings/workspace"),
  setWorkspaceDir: (dir: string) => api<{ dir: string; isDefault: boolean; default: string }>("PUT", "/api/settings/workspace", { dir }),
  agentPrompts: (id: string) => api<{ prompts: PromptInfo[] }>("GET", `/api/agents/${enc(id)}/prompts`),
  agentSkills: (id: string) => api<{ skills: { name: string; description?: string; source?: string }[] }>("GET", `/api/agents/${enc(id)}/skills`),
  agentToolGroups: (id: string) =>
    api<{ groups: { name: string; count: number; summary: string; deferred: boolean; installed?: boolean }[] | null; deferredTokens: number }>("GET", `/api/agents/${enc(id)}/tools`),
  vault: () => api<{ items: VaultItem[] }>("GET", "/api/vault"),
  addSecret: (name: string, value: string) => api<VaultItem>("POST", "/api/vault", { name, value }),
  replaceSecret: (id: string, value: string) => api<VaultItem>("PUT", "/api/vault", { id, value }),
  deleteSecret: (id: string) => api("DELETE", `/api/vault?id=${encodeURIComponent(id)}`),
  system: () => api<SystemInfo>("GET", "/api/system"),
};

export function refString(r: ModelRef, withEffort = true): string {
  return `${r.provider}/${r.model}${withEffort && r.effort ? "@" + r.effort : ""}`;
}

export function parseRef(s: string): ModelRef | null {
  let effort: string | undefined;
  const at = s.lastIndexOf("@");
  if (at > 0) {
    effort = s.slice(at + 1);
    s = s.slice(0, at);
  }
  const i = s.indexOf("/");
  if (i <= 0 || i === s.length - 1) return null;
  return { provider: s.slice(0, i), model: s.slice(i + 1), effort };
}

/** A provider offered in the add dialog. */
export interface ProviderPreset {
  id: string;
  name: string;
  api: string;
  baseURL: string;
  keyURL?: string;
  icon?: string;
  /** "" = model vendor, "platform" = aggregator / inference platform */
  group?: string;
  modelHint?: string;
  /** what it offers, in a few words */
  desc?: string;
}

/** A provider added from the UI (GET /api/providers). */
export interface UserProvider {
  id: string;
  name?: string;
  api: string;
  preset?: string;
  baseURL?: string;
  keyRef?: string;
  models?: string[];
  kinds?: Record<string, ModelKind>;
  icon?: string;
  masked?: string;
  active: boolean;
  error?: string;
}

/** PATCH /api/providers/{id}: omitted fields stay; a blank apiKey keeps the stored key. */
export interface ProviderPatch {
  name?: string;
  icon?: string;
  baseURL?: string;
  apiKey?: string;
  models?: string[];
  kinds?: Record<string, ModelKind>;
}

/** GET /api/providers/magpie/models */
export interface MagpieChoice {
  running: boolean;
  models: ModelInfo[];
  /** null = every model magpie lists */
  enabled: string[] | null;
}

/** GET /api/model-catalog */
export interface CatalogStatus {
  settings: { sources: string[]; autoRefresh: boolean };
  sources: { url: string; format?: string; fetched?: string; models: number; origin: "remote" | "embedded" | ""; error?: string }[];
  models: number;
  updated: string;
  refreshing: boolean;
  lastAttempt?: string;
  embeddedAt?: string;
}

export interface ProviderInput {
  id: string;
  name?: string;
  preset?: string;
  api: string;
  baseURL: string;
  apiKey?: string;
  /** vendor icon key, "mono" for a letter tile */
  icon?: string;
}

export interface ProviderTestResult {
  ok: boolean;
  latencyMs: number;
  models: ModelInfo[];
  error?: string;
  /** the key was accepted but models cannot be listed: type ids */
  manualModels?: boolean;
}

/** GET /api/unfurl: what a link card shows (features/linkpreview). */
export interface LinkCard {
  url: string;
  kind: "page" | "video" | "image" | "audio";
  title?: string;
  description?: string;
  siteName?: string;
  image?: string;
  icon?: string;
  embed?: { provider: string; src: string; aspect: number };
  media?: string;
}

// ---------------------------------------------------------------- 模型擂台 (features/arena)

/** One model's record in one task category (GET /api/arena/stats). */
export interface ArenaModelStat {
  model: string;
  runs: number;
  wins: number;
  avgCostUsd: number;
  priced: boolean;
  avgMs: number;
}
export interface ArenaCategoryStat {
  category: string;
  arenas: number;
  picked: number;
  models: ArenaModelStat[];
}
export interface ArenaStats {
  arenas: number;
  picked: number;
  categories: ArenaCategoryStat[];
}
/** The model usually picked for tasks like this one (GET /api/arena/suggest). */
export interface ArenaSuggestion {
  category: string;
  model?: string;
  wins?: number;
  picked?: number;
  avgCostUsd?: number;
  priced?: boolean;
  ranked: string[];
}

export const ArenaAPI = {
  start: (session: string, body: { text: string; models: string[]; agent?: string }) => api<{ id: string; category: string }>("POST", `/api/sessions/${encodeURIComponent(session)}/arena`, body),
  pick: (session: string, arena: string, index: number) => api("POST", `/api/sessions/${encodeURIComponent(session)}/arena/${encodeURIComponent(arena)}/pick`, { index }),
  stop: (session: string, arena: string) => api("POST", `/api/sessions/${encodeURIComponent(session)}/arena/${encodeURIComponent(arena)}/stop`),
  rerun: (session: string, arena: string, index: number) => api("POST", `/api/sessions/${encodeURIComponent(session)}/arena/${encodeURIComponent(arena)}/rerun`, { index }),
  stats: () => api<ArenaStats>("GET", "/api/arena/stats"),
  suggest: (text: string) => api<ArenaSuggestion>("GET", `/api/arena/suggest?text=${encodeURIComponent(text.slice(0, 2000))}`),
};

// ---- 记忆 page and agent 档案 (features/memory panel.go, features/usage profile.go)

/** One thing an agent or a team knows: a MEMORY.md / TEAM.md line, a task
 *  progress file or a reference note. */
export interface MemEntry {
  kind: "memory" | "team" | "task" | "note";
  agent?: string;
  team?: string;
  text: string;
  section?: string;
  name?: string;
  line?: number;
  raw?: string;
  by?: string;
  /** a day (YYYY-MM-DD) or RFC 3339 */
  saved?: string;
  used?: string;
  session?: string;
  size?: number;
}
export interface MemList {
  entries: MemEntry[];
  agents: { id: string; name: string; team: string }[];
  teams: { id: string; name: string }[];
}
/** what identifies an entry when changing it */
export type MemRef = Pick<MemEntry, "kind" | "agent" | "team" | "line" | "raw" | "name">;
export interface AgentProfile {
  agent: string;
  ready: boolean;
  turns: number;
  failed: number;
  toolCalls: number;
  toolErrors: number;
  tools: { name: string; n: number }[];
  asked: number;
  approved: number;
  rejected: number;
  tokens: number;
  spentUsd: number;
  savedUsd: number;
  models: { model: string; provider: string; kind: string; tokens: number; messages: number; spentUsd: number; savedUsd: number }[];
  activeDays: number;
  streak: number;
  bestStreak: number;
  first?: string;
  last?: string;
  /** turns per day, the last 28 days, oldest first */
  recent: number[];
  strengths: { kind: string; n: number; share: number }[];
  badges: { id: string; earned: boolean; progress: number; goal: number; at?: string }[];
}
const memRef = (r: MemRef) => ({ kind: r.kind, agent: r.agent, team: r.team, line: r.line, raw: r.raw, name: r.name });
export const MemAPI = {
  list: () => api<MemList>("GET", "/api/memories"),
  update: (r: MemRef, text: string) => api<{ text: string }>("PUT", "/api/memories", { ...memRef(r), text }),
  remove: (r: MemRef) => api<{ ok: boolean }>("POST", "/api/memories/delete", memRef(r)),
  /** 这条不对: removed, or replaced by the correction; tell = the agent hears of it */
  wrong: (r: MemRef, correction: string, tell: boolean) => api<{ ok: boolean; told: boolean }>("POST", "/api/memories/wrong", { ...memRef(r), correction, tell }),
  profile: (agent: string) => api<AgentProfile>("GET", `/api/agents/${encodeURIComponent(agent)}/profile`),
};
