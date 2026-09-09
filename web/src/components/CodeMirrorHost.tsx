import { useEffect, useRef } from "react";
import { Compartment, EditorState, type Extension, Prec } from "@codemirror/state";
import { EditorView, keymap, lineNumbers } from "@codemirror/view";
import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
import { StreamLanguage } from "@codemirror/language";
import { oneDark } from "@codemirror/theme-one-dark";

export interface CodeMirrorHostProps {
  path: string;
  initialDoc: string;
  language: string;
  onReady: () => void;
  onDirtyChange: (dirty: boolean) => void;
  onSave: (text: string) => Promise<void>;
}

/**
 * Resolve the language support for a mode name. Every pack is a dynamic
 * import() so the build splits it into its own chunk and the working view
 * only pays for the modes it actually opens. `plaintext` needs nothing.
 */
async function languageExtension(language: string): Promise<Extension | null> {
  switch (language) {
    case "javascript":
      return (await import("@codemirror/lang-javascript")).javascript({
        jsx: true,
      });
    case "typescript":
      return (await import("@codemirror/lang-javascript")).javascript({
        jsx: true,
        typescript: true,
      });
    case "python":
      return (await import("@codemirror/lang-python")).python();
    case "rust":
      return (await import("@codemirror/lang-rust")).rust();
    case "go":
      return (await import("@codemirror/lang-go")).go();
    case "json":
      return (await import("@codemirror/lang-json")).json();
    case "markdown":
      return (await import("@codemirror/lang-markdown")).markdown();
    case "html":
      return (await import("@codemirror/lang-html")).html();
    case "css":
      return (await import("@codemirror/lang-css")).css();
    case "toml": {
      const { toml } = await import("@codemirror/legacy-modes/mode/toml");
      return StreamLanguage.define(toml);
    }
    default:
      return null;
  }
}

export default function CodeMirrorHost({
  path,
  initialDoc,
  language,
  onReady,
  onDirtyChange,
  onSave,
}: CodeMirrorHostProps) {
  const host = useRef<HTMLDivElement>(null);
  // Keep the latest callbacks reachable without re-running the mount effect.
  const cbs = useRef({ onReady, onDirtyChange, onSave });
  cbs.current = { onReady, onDirtyChange, onSave };

  useEffect(() => {
    const parent = host.current;
    if (!parent) return;

    // The baseline the buffer is compared against: the last saved text, or
    // the loaded content before the first save.
    const saved = { text: initialDoc };
    let timer: ReturnType<typeof setTimeout> | undefined;
    const langSlot = new Compartment();

    const flagDirty = () => {
      if (timer) clearTimeout(timer);
      timer = setTimeout(() => {
        cbs.current.onDirtyChange(view.state.doc.toString() !== saved.text);
      }, 150);
    };

    const saveKey = Prec.highest(
      keymap.of([
        {
          key: "Mod-s",
          preventDefault: true,
          run: (v) => {
            const text = v.state.doc.toString();
            // Only move the baseline and clear the dirty flag once the write
            // actually lands: a rejected save must leave the tab dirty so the
            // edits are not silently lost on close.
            void cbs.current.onSave(text).then(
              () => {
                saved.text = text;
                if (timer) clearTimeout(timer);
                cbs.current.onDirtyChange(false);
              },
              () => {
                // Save failed; keep `saved.text` and the dirty marker as they
                // were. EditorPane surfaces the error to the user.
              },
            );
            return true;
          },
        },
      ]),
    );

    const state = EditorState.create({
      doc: initialDoc,
      extensions: [
        lineNumbers(),
        history(),
        keymap.of([...defaultKeymap, ...historyKeymap]),
        saveKey,
        oneDark,
        langSlot.of([]),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) flagDirty();
        }),
      ],
    });

    const view = new EditorView({ state, parent });
    cbs.current.onReady();

    let cancelled = false;
    void languageExtension(language).then((ext) => {
      if (cancelled || !ext) return;
      view.dispatch({ effects: langSlot.reconfigure(ext) });
    });

    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      view.destroy();
    };
    // The tab's identity is `path`; content/callbacks are read through refs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);

  return <div className="cm-host" ref={host} />;
}
