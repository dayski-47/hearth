import { lazy, Suspense } from "react";
import { useStore } from "../store";
import { writeFileContent } from "../api/files";
import { basename } from "../lib/fsPath";
import "./EditorPane.css";

// CodeMirror and every language pack stay out of the entry chunk: the editor
// is only fetched once a file is actually opened.
const CodeMirrorHost = lazy(() => import("./CodeMirrorHost"));

export default function EditorPane() {
  const tabs = useStore((s) => s.editor.tabs);
  const activePath = useStore((s) => s.editor.activePath);
  const workspaceId = useStore((s) => s.editor.workspaceId);
  const closeTab = useStore((s) => s.closeTab);
  const setActiveTab = useStore((s) => s.setActiveTab);
  const markDirty = useStore((s) => s.markDirty);
  const consumeInitialDoc = useStore((s) => s.consumeInitialDoc);
  const recordSaved = useStore((s) => s.recordSaved);
  const retryOpen = useStore((s) => s.retryOpen);
  const reloadTab = useStore((s) => s.reloadTab);
  const markChangedOnDisk = useStore((s) => s.markChangedOnDisk);

  if (tabs.length === 0) {
    return (
      <div className="pane pane-editor editor-empty">
        Open a file from the tree.
      </div>
    );
  }

  function guardedClose(path: string, dirty: boolean) {
    if (dirty && !confirm(`Discard changes to ${basename(path)}?`)) return;
    closeTab(path);
  }

  async function save(path: string, text: string) {
    if (!workspaceId) return;
    try {
      await writeFileContent(workspaceId, path, text);
      markDirty(path, false);
      recordSaved(path, text);
    } catch (e) {
      // Tell the user, then re-throw so CodeMirrorHost keeps the tab dirty and
      // its saved baseline unchanged - the edits must not look committed.
      const msg = e instanceof Error ? e.message : "could not save the file";
      alert(`Could not save ${basename(path)}: ${msg}`);
      throw e;
    }
  }

  return (
    <div className="pane pane-editor editor-pane">
      <div className="editor-tabs" role="tablist">
        {tabs.map((t) => (
          <div
            key={t.path}
            role="tab"
            aria-selected={t.path === activePath}
            className={t.path === activePath ? "et-tab active" : "et-tab"}
            onClick={() => setActiveTab(t.path)}
            onAuxClick={(e) => {
              if (e.button === 1) guardedClose(t.path, t.dirty);
            }}
          >
            {t.dirty && <span className="et-dot" aria-hidden="true" />}
            <span className="et-name">{basename(t.path)}</span>
            <button
              className="et-x"
              aria-label={`Close ${basename(t.path)}`}
              onClick={(e) => {
                e.stopPropagation();
                guardedClose(t.path, t.dirty);
              }}
            >
              &times;
            </button>
          </div>
        ))}
      </div>

      {tabs.map((t) => (
        <div key={t.path} className="editor-body" hidden={t.path !== activePath}>
          {t.deletedOnDisk && (
            <div className="editor-bar" role="status">
              This file was deleted on disk. Save to recreate it.
            </div>
          )}
          {t.changedOnDisk && (
            <div className="editor-bar" role="status">
              Changed on disk.{" "}
              <button onClick={() => void reloadTab(t.path)}>
                Reload, lose edits
              </button>{" "}
              <button onClick={() => markChangedOnDisk(t.path, false)}>
                Keep mine
              </button>
            </div>
          )}
          {t.openError ? (
            <div className="editor-error" role="alert">
              <p>{t.openError}</p>
              <button onClick={() => void retryOpen(t.path)}>Retry</button>
            </div>
          ) : !t.loaded ? (
            <div className="editor-loading">Loading the file...</div>
          ) : (
            <Suspense
              fallback={<div className="editor-loading">Loading the editor...</div>}
            >
              <CodeMirrorHost
                key={`${t.path}#${t.reloadNonce}`}
                path={t.path}
                initialDoc={t.initialDoc ?? ""}
                language={t.language}
                onReady={() => consumeInitialDoc(t.path)}
                onDirtyChange={(d) => markDirty(t.path, d)}
                onSave={(text) => save(t.path, text)}
              />
            </Suspense>
          )}
        </div>
      ))}
    </div>
  );
}
