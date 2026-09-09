import { useState } from "react";
import { useStore } from "../store";
import type { FileNode } from "../store/tree";
import "./FileTree.css";

function parentOf(path: string): string {
  return path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "";
}

function Row({
  node,
  depth,
  onOpen,
}: {
  node: FileNode;
  depth: number;
  onOpen: (p: string) => void;
}) {
  const expanded = useStore((s) => s.tree.expanded.has(node.path));
  const children = useStore((s) => s.tree.children[node.path]);
  const loading = useStore((s) => s.tree.loading.has(node.path));
  const toggleDir = useStore((s) => s.toggleDir);
  const createNode = useStore((s) => s.createNode);
  const renameNode = useStore((s) => s.renameNode);
  const deleteNode = useStore((s) => s.deleteNode);
  const [renaming, setRenaming] = useState(false);

  const indent = { paddingLeft: 8 + depth * 12 };

  if (renaming) {
    return (
      <form
        className="ft-row"
        style={indent}
        onSubmit={(e) => {
          e.preventDefault();
          const name = new FormData(e.currentTarget).get("n") as string;
          const parent = parentOf(node.path);
          if (name && name !== node.name) {
            void renameNode(
              node.path,
              parent ? `${parent}/${name}` : name,
              node.is_dir,
            );
          }
          setRenaming(false);
        }}
      >
        <input
          name="n"
          defaultValue={node.name}
          autoFocus
          aria-label="new name"
          onBlur={() => setRenaming(false)}
        />
      </form>
    );
  }

  return (
    <>
      <div
        className="ft-row"
        style={indent}
        onClick={() =>
          node.is_dir ? void toggleDir(node.path) : onOpen(node.path)
        }
      >
        <span className="ft-chevron">
          {node.is_dir ? (expanded ? "▾" : "▸") : ""}
        </span>
        <span className="ft-name">{node.name}</span>
        <span className="ft-actions" onClick={(e) => e.stopPropagation()}>
          {node.is_dir && (
            <>
              <button
                title="New file"
                onClick={() => {
                  const n = prompt("file name");
                  if (n) void createNode(node.path, n, false);
                }}
              >
                +f
              </button>
              <button
                title="New folder"
                onClick={() => {
                  const n = prompt("folder name");
                  if (n) void createNode(node.path, n, true);
                }}
              >
                +d
              </button>
            </>
          )}
          <button title="Rename" onClick={() => setRenaming(true)}>
            ren
          </button>
          <button
            title="Delete"
            onClick={() => {
              if (confirm(`Delete ${node.name}?`)) void deleteNode(node.path);
            }}
          >
            del
          </button>
        </span>
      </div>
      {node.is_dir && expanded && (
        <div className="ft-children">
          {loading && (
            <div
              className="ft-row ft-dim"
              style={{ paddingLeft: 8 + (depth + 1) * 12 }}
            >
              ...
            </div>
          )}
          {(children ?? []).map((c) => (
            <Row key={c.path} node={c} depth={depth + 1} onOpen={onOpen} />
          ))}
        </div>
      )}
    </>
  );
}

export default function FileTree({
  onOpen,
}: {
  onOpen: (path: string) => void;
}) {
  const root = useStore((s) => s.tree.children[""]);
  const error = useStore((s) => s.tree.error);
  const createNode = useStore((s) => s.createNode);
  return (
    <div className="pane pane-files file-tree">
      <div className="ft-toolbar">
        <button
          onClick={() => {
            const n = prompt("file name");
            if (n) void createNode("", n, false);
          }}
        >
          + file
        </button>
        <button
          onClick={() => {
            const n = prompt("folder name");
            if (n) void createNode("", n, true);
          }}
        >
          + folder
        </button>
      </div>
      {error && (
        <div className="ft-error" role="alert">
          {error}
        </div>
      )}
      <div className="ft-scroll">
        {(root ?? []).map((n) => (
          <Row key={n.path} node={n} depth={0} onOpen={onOpen} />
        ))}
      </div>
    </div>
  );
}
