import { Bell, Boxes, Library, Cpu, Keyboard, KeyRound, ListChecks, MessagesSquare, MousePointerClick, Puzzle, Server, Settings as Gear, User, Workflow, Users } from "lucide-react";
import type { ReactNode } from "react";
import type { Key } from "../../i18n";
import type { SettingsSection } from "../../store";

// The settings sections, apart from the pages so the palette can list them
// without loading every page.
export const NAV: { group: Key; items: { id: SettingsSection; icon: ReactNode }[] }[] = [
  {
    group: "set.group.you",
    items: [
      { id: "general", icon: <Gear size={16} /> },
      { id: "profile", icon: <User size={16} /> },
      { id: "vault", icon: <KeyRound size={16} /> },
      { id: "notify", icon: <Bell size={16} /> },
      { id: "keys", icon: <Keyboard size={16} /> },
    ],
  },
  {
    group: "set.group.agent",
    items: [
      { id: "models", icon: <Cpu size={16} /> },
      { id: "market", icon: <Puzzle size={16} /> },
      { id: "library", icon: <Library size={16} /> },
      { id: "computer", icon: <MousePointerClick size={16} /> },
      { id: "auto", icon: <Workflow size={16} /> },
      { id: "approvals", icon: <ListChecks size={16} /> },
    ],
  },
  {
    group: "set.group.device",
    items: [
      { id: "teams", icon: <Boxes size={16} /> },
      { id: "team", icon: <Users size={16} /> },
      { id: "im", icon: <MessagesSquare size={16} /> },
      { id: "hosts", icon: <Server size={16} /> },
    ],
  },
];

export const SECTION_ICONS: Record<SettingsSection, ReactNode> = Object.fromEntries(NAV.flatMap((g) => g.items.map((i) => [i.id, i.icon]))) as any;
export const SECTIONS: SettingsSection[] = NAV.flatMap((g) => g.items.map((i) => i.id));
