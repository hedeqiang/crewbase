import { useStore } from "../store";
import { t } from "../i18n";
import { LibraryPage } from "./Library";
import { Automation } from "./settings/Automation";
import { ListToggle } from "./Header";
import { MemoryPage } from "./Memory";

/** 工作台: the pages used every day (文档库, 自动化, 记忆), in the main
 *  column beside the conversation list instead of inside settings. */
export function Desk() {
  const page = useStore((s) => s.desk);
  const listOpen = useStore((s) => s.listOpen);
  const manage = useStore((s) => s.tier === "desktop");
  // the library brings its own header and panes
  if (page === "library" && manage) return <LibraryPage />;
  return (
    <div className="center desk" data-desk={page}>
      {!listOpen && (
        <div className="chead deskhead">
          <ListToggle />
        </div>
      )}
      <div className="vw">
        {page === "auto" && manage && <Automation />}
        {page === "memory" && manage && <MemoryPage />}
        {!manage && <p className="tiernote">{t("tier.mobileNote")}</p>}
      </div>
    </div>
  );
}
