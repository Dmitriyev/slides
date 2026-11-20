import React, { useEffect, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  ArrowLeft,
  ArrowRight,
  Plus,
  Upload,
  FileText,
  X,
  Sparkles,
  LockKeyhole,
  Check,
  Undo2,
  FolderOpen,
  Trash2,
  Pencil,
  ChevronRight,
  Layers,
  MessageSquare,
} from "lucide-react";
import "./style.css";

type Settings = {
  detail: number;
  complexity: number;
  tone: number;
  charts: number;
};
type Chart = {
  title: string;
  unit: string;
  data: { label: string; value: number; evidence: string }[];
};
type Slide = {
  id: string;
  title: string;
  paragraphs: string[];
  chart: Chart | null;
  note: string;
  locked: boolean;
  settings: Settings;
};
type Deck = { title: string; slides: Slide[]; source: string };
type Project = {
  id: string;
  name: string;
  updatedAt: string;
  description: string;
  count: number;
  settings: Settings;
  deck: Deck | null;
  selected: number;
};
const defaults: Settings = { detail: 1, complexity: 1, tone: 1, charts: 1 };
const STORAGE = "slide-tuner.projects.v1";
const controls: { key: keyof Settings; label: string; levels: string[] }[] = [
  {
    key: "detail",
    label: "Level of detail",
    levels: ["Brief", "Balanced", "Detailed"],
  },
  {
    key: "complexity",
    label: "Language complexity",
    levels: ["Simple", "Business", "Expert"],
  },
  {
    key: "tone",
    label: "Tone",
    levels: ["Conversational", "Neutral", "Formal"],
  },
  {
    key: "charts",
    label: "Charts and diagrams",
    levels: ["Text only", "When relevant", "More charts"],
  },
];
type ChatMessage = { role: "user" | "assistant"; text: string };
const fresh = (): Project => ({
  id: crypto.randomUUID(),
  name: "New presentation",
  updatedAt: new Date().toISOString(),
  description: "",
  count: 6,
  settings: { ...defaults },
  deck: null,
  selected: 0,
});
function readProjects(): { projects: Project[]; error: string } {
  try {
    const raw = localStorage.getItem(STORAGE);
    if (!raw) return { projects: [], error: "" };
    const data = JSON.parse(raw);
    if (
      !Array.isArray(data) ||
      !data.every(
        (p) =>
          typeof p.id === "string" &&
          typeof p.name === "string" &&
          p.settings &&
          typeof p.description === "string" &&
          (!p.deck || Array.isArray(p.deck.slides)),
      )
    )
      throw Error();
    return { projects: data, error: "" };
  } catch {
    return {
      projects: [],
      error:
        "Could not load project history. Your stored data has not been overwritten. Download a backup before continuing.",
    };
  }
}
async function api(path: string, body: FormData | object): Promise<Deck> {
  const response = await fetch("/api/v1/presentations/" + path, {
    method: "POST",
    headers:
      body instanceof FormData ? {} : { "Content-Type": "application/json" },
    body: body instanceof FormData ? body : JSON.stringify(body),
  });
  if (!response.ok) {
    let message = `API error (${response.status})`;
    try {
      const data = await response.json();
      message =
        typeof data.detail === "string"
          ? data.detail
          : "Check your settings and try again";
    } catch {
      /* proxy can return non-JSON */
    }
    throw Error(message);
  }
  return response.json();
}
function startProject(saved: Project[]): Project | null {
  if (!saved.length) return fresh();
  const latest = [...saved].sort((a, b) =>
    b.updatedAt.localeCompare(a.updatedAt),
  )[0];
  return structuredClone(latest);
}
function App() {
  const initial = useRef(readProjects());
  const [projects, setProjects] = useState<Project[]>(initial.current.projects);
  const [project, setProject] = useState<Project | null>(() =>
    initial.current.error ? null : startProject(initial.current.projects),
  );
  const [files, setFiles] = useState<File[]>([]);
  const [scope, setScope] = useState<"slide" | "deck">("deck");
  const [busy, setBusy] = useState(false);
  const [pendingRewrite, setPendingRewrite] = useState<{
    projectId: string;
    scope: "slide" | "deck";
    slideId?: string;
  } | null>(null);
  const [error, setError] = useState("");
  const [storageError, setStorageError] = useState(initial.current.error);
  const [history, setHistory] = useState<Deck[]>([]);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [deleteId, setDeleteId] = useState<string | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const deck = project?.deck;
  const slide = deck?.slides[project?.selected || 0];
  const targets = scope === "deck" ? deck?.slides || [] : slide ? [slide] : [];
  const allLocked = targets.length > 0 && targets.every((s) => s.locked);
  const someLocked = targets.some((s) => s.locked);

  // Keep the project draft and all manual edits in the same persisted record.
  useEffect(() => {
    if (!project) return;
    setProjects((old) => [project, ...old.filter((p) => p.id !== project.id)]);
  }, [project]);
  useEffect(() => {
    if (initial.current.error) return;
    try {
      localStorage.setItem(STORAGE, JSON.stringify(projects));
      setStorageError("");
    } catch {
      setStorageError(
        "Could not save changes: browser storage is unavailable or full. Download a backup.",
      );
    }
  }, [projects]);

  // Only slider input schedules a rewrite, never navigation or generated content.
  useEffect(() => {
    if (!pendingRewrite) return;
    if (
      !deck || !project ||
      pendingRewrite.projectId !== project.id ||
      pendingRewrite.scope !== scope ||
      pendingRewrite.slideId !== slide?.id
    ) {
      setPendingRewrite(null);
      return;
    }
    if (busy) return;
    const timer = window.setTimeout(() => {
      setPendingRewrite(null);
      void run(true);
    }, 600);
    return () => window.clearTimeout(timer);
  }, [pendingRewrite, project, scope, busy]);

  function patch(change: Partial<Project>) {
    setProject(
      (p) => p && { ...p, ...change, updatedAt: new Date().toISOString() },
    );
  }
  function backup() {
    const blob = new Blob(
      [
        JSON.stringify(
          {
            projects,
            unreadableStorage: initial.current.error
              ? localStorage.getItem(STORAGE)
              : undefined,
          },
          null,
          2,
        ),
      ],
      { type: "application/json" },
    );
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "slide-tuner-projects.json";
    a.click();
    URL.revokeObjectURL(url);
  }
  function open(p: Project) {
    setProject(structuredClone(p));
    setFiles([]);
    setHistory([]);
    setMessages([]);
    setError("");
    setScope("deck");
  }
  function changeSlide(change: Partial<Slide>) {
    if (!deck || !slide) return;
    patch({
      deck: {
        ...deck,
        slides: deck.slides.map((s) =>
          s.id === slide.id ? { ...s, ...change } : s,
        ),
      },
    });
  }
  function addFiles(incoming: File[]) {
    if (
      files.length + incoming.length > 8 ||
      [...files, ...incoming].reduce((n, f) => n + f.size, 0) > 20 * 1024 * 1024
    ) {
      setError("Up to 8 files, 20 MB total");
      return;
    }
    setFiles((old) => [...old, ...incoming]);
    setError("");
  }
  async function run(rewrite = false) {
    if (!project || busy) return;
    setPendingRewrite(null);
    setBusy(true);
    setError("");
    try {
      let next: Deck;
      if (rewrite && deck) {
        next = await api("rewrite", {
          deck,
          scope,
          slide_id: slide?.id,
          settings: project.settings,
        });
        setHistory((old) => [...old.slice(-9), structuredClone(deck)]);
      } else {
        const prompt = project.description.trim();
        setMessages((old) => [
          ...old,
          {
            role: "user",
            text:
              prompt ||
              files.map((f) => f.name).join(", ") ||
              "Build the content from the uploaded materials",
          },
        ]);
        const data = new FormData();
        data.set("description", project.description);
        data.set("slide_count", String(project.count || 6));
        data.set("settings", JSON.stringify(project.settings));
        files.forEach((f) => data.append("files", f));
        next = await api("generate", data);
      }
      patch({
        deck: next,
        name: project.name === "New presentation" ? next.title : project.name,
      });
      setFiles([]);
    } catch (e) {
      const message =
        e instanceof Error ? e.message : "Could not connect to the server";
      setError(message);
      if (!rewrite)
        setMessages((old) => [...old, { role: "assistant", text: message }]);
    } finally {
      setBusy(false);
    }
  }
  return (
    <>
      <header>
        <button
          className="brand"
          onClick={() => {
            if (!busy) setProject(null);
          }}
        >
          <span className="brand-icon">
            <Layers size={20} />
          </span>
          slide<span className="brand-light">tuner</span>
        </button>
        <button
          className="subtle"
          disabled={busy}
          onClick={() => setProject(null)}
        >
          <FolderOpen size={16} /> My projects{" "}
          <span className="badge">{projects.length}</span>
        </button>
      </header>
      {storageError && (
        <div className="alert" role="alert">
          {storageError} <button onClick={backup}>Download backup</button>
        </div>
      )}
      {!project ? (
        <main className="projects-page">
          <div className="page-heading">
            <div>
              <h1>
                My presentations<span className="green-dot">.</span>
              </h1>
            </div>
            <button className="primary" onClick={() => open(fresh())}>
              <Plus size={18} /> New project
            </button>
          </div>
          {!projects.length ? (
            <div className="empty">
              <span className="empty-icon">
                <FileText size={32} />
              </span>
              <h2>No presentations</h2>
              <button className="primary" onClick={() => open(fresh())}>
                Create presentation <ArrowRight size={17} />
              </button>
            </div>
          ) : (
            <div className="project-grid">
              {projects.map((p) => (
                <article className="project-card" key={p.id}>
                  <button className="project-open" onClick={() => open(p)}>
                    <div className="project-cover">
                      <span>
                        SLIDE TUNER /{" "}
                        {p.deck
                          ? String(p.deck.slides.length).padStart(2, "0") +
                            " SLIDES"
                          : "DRAFT"}
                      </span>
                      <h2>{p.deck?.slides[0]?.title || p.name}</h2>
                      <div className="cover-line" />
                    </div>
                    <h3>{p.name}</h3>
                    <p>
                      {new Date(p.updatedAt).toLocaleString("en-US", {
                        dateStyle: "medium",
                        timeStyle: "short",
                      })}{" "}
                      <ChevronRight size={15} />
                    </p>
                  </button>
                  <div className="project-actions">
                    <button
                      className="subtle"
                      onClick={() => {
                        const name = prompt("Project name", p.name);
                        if (name?.trim())
                          setProjects((old) =>
                            old.map((item) =>
                              item.id === p.id
                                ? {
                                    ...item,
                                    name: name.trim(),
                                    updatedAt: new Date().toISOString(),
                                  }
                                : item,
                            ),
                          );
                      }}
                    >
                      <Pencil size={14} /> Rename
                    </button>
                    <button
                      className="icon-button"
                      aria-label={"Delete " + p.name}
                      onClick={() => setDeleteId(p.id)}
                    >
                      <Trash2 size={15} />
                    </button>
                  </div>
                  {deleteId === p.id && (
                    <div className="delete-confirm">
                      Delete project?
                      <button
                        onClick={() => {
                          setProjects((old) =>
                            old.filter((item) => item.id !== p.id),
                          );
                          setDeleteId(null);
                        }}
                      >
                        Delete
                      </button>
                      <button onClick={() => setDeleteId(null)}>Cancel</button>
                    </div>
                  )}
                </article>
              ))}
            </div>
          )}
          <div className="storage-caption">
            <button onClick={backup}>Download backup</button>
          </div>
        </main>
      ) : !deck ? (
        <main className="workspace-page">
          <div className="workspace">
            <section className="input-card upload-pane">
              <div className="section-label">MATERIALS</div>
              <div
                className="drop-zone"
                onDragOver={(e) => e.preventDefault()}
                onDrop={(e) => {
                  e.preventDefault();
                  if (!busy) addFiles(Array.from(e.dataTransfer.files));
                }}
              >
                <Upload size={23} />
                <strong>Drop your materials here</strong>
                <span>
                  or{" "}
                  <button
                    disabled={busy}
                    onClick={() => fileInput.current?.click()}
                  >
                    choose files
                  </button>
                </span>
                <small>PDF, DOCX, PPTX, TXT, MD, CSV · up to 20 MB</small>
                <input
                  ref={fileInput}
                  type="file"
                  multiple
                  accept=".pdf,.docx,.pptx,.txt,.md,.csv"
                  hidden
                  onChange={(e) => {
                    addFiles(Array.from(e.target.files || []));
                    e.target.value = "";
                  }}
                />
              </div>
              {files.map((f, i) => (
                <div className="file-row" key={i}>
                  <FileText size={16} />
                  {f.name}
                  <small>{Math.ceil(f.size / 1024)} KB</small>
                  <button
                    disabled={busy}
                    aria-label={"Remove " + f.name}
                    onClick={() =>
                      setFiles((old) => old.filter((_, n) => n !== i))
                    }
                  >
                    <X size={16} />
                  </button>
                </div>
              ))}
            </section>
            <section className="chat-pane">
              <div className="settings-heading">
                <span className="settings-icon">
                  <MessageSquare size={18} />
                </span>
                <div>
                  <h2>Chat</h2>
                </div>
              </div>
              <div className="chat-log">
                {messages.map((m, i) => (
                  <div className={"chat-bubble " + m.role} key={i}>
                    {m.text}
                  </div>
                ))}
              </div>
              {error && (
                <div role="alert" className="alert">
                  {error}
                </div>
              )}
              <div className="chat-composer">
                <label htmlFor="description">Brief and context</label>
                <textarea
                  id="description"
                  maxLength={20000}
                  disabled={busy}
                  value={project.description}
                  onChange={(e) => patch({ description: e.target.value })}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && !e.shiftKey) {
                      e.preventDefault();
                      if (
                        !busy &&
                        (files.length || project.description.trim())
                      )
                        run();
                    }
                  }}
                  placeholder="For example: a presentation for the team about quarterly results. Highlight the key changes, explain the reasons and suggest next steps…"
                />
                <button
                  className="primary"
                  disabled={
                    busy || (!files.length && !project.description.trim())
                  }
                  onClick={() => run()}
                >
                  {busy ? "Generating content…" : "Generate content"}
                  {!busy && <ArrowRight size={18} />}
                </button>
              </div>
            </section>
          </div>
        </main>
      ) : (
        <main className="editor">
          <div className="editor-heading">
            <div>
              <button
                disabled={busy}
                className="back"
                onClick={() => setProject(null)}
              >
                <ArrowLeft size={15} /> All projects
              </button>
              <input
                className="project-name"
                aria-label="Project name"
                disabled={busy}
                value={project.name}
                onChange={(e) => patch({ name: e.target.value })}
              />
            </div>
            <span className="saved">
              <Check size={14} />
              {storageError ? "Not saved" : "Saved"}
            </span>
          </div>
          <div className="editor-grid">
            <aside className="filmstrip">
              <div className="section-label">
                SLIDES <span>{deck.slides.length}</span>
              </div>
              {deck.slides.map((s, i) => (
                <button
                  disabled={busy}
                  key={s.id}
                  className={
                    "thumbnail " + (i === project.selected ? "selected" : "")
                  }
                  onClick={() => {
                    patch({ selected: i, settings: s.settings });
                    setError("");
                  }}
                >
                  <span className="slide-index">
                    {String(i + 1).padStart(2, "0")}
                    {s.locked && <LockKeyhole size={11} />}
                  </span>
                  <strong>{s.title}</strong>
                  <div className="tiny-lines">
                    <i />
                    <i />
                    <i />
                  </div>
                </button>
              ))}
            </aside>
            <section className="canvas-area">
              <div className="canvas-toolbar">
                <span>
                  CONTENT <span className="separator">/</span> SLIDE{" "}
                  {(project.selected || 0) + 1}
                </span>
                <button
                  className="subtle"
                  disabled={busy || !history.length}
                  onClick={() => {
                    patch({ deck: history[history.length - 1] });
                    setHistory((old) => old.slice(0, -1));
                  }}
                >
                  <Undo2 size={15} /> Undo generation
                </button>
              </div>
              {slide && (
                <article className="slide-canvas">
                  <div className="slide-topline">
                    <span>SLIDE TUNER</span>
                    <span>
                      {String((project.selected || 0) + 1).padStart(2, "0")} /{" "}
                      {String(deck.slides.length).padStart(2, "0")}
                    </span>
                  </div>
                  <textarea
                    className="slide-title"
                    aria-label="Slide title"
                    disabled={busy}
                    value={slide.title}
                    onChange={(e) => changeSlide({ title: e.target.value })}
                  />
                  <div className="slide-content">
                    {slide.paragraphs.map((p, i) => (
                      <textarea
                        key={i}
                        aria-label={"Paragraph " + (i + 1)}
                        disabled={busy}
                        value={p}
                        onChange={(e) =>
                          changeSlide({
                            paragraphs: slide.paragraphs.map((text, n) =>
                              n === i ? e.target.value : text,
                            ),
                          })
                        }
                      />
                    ))}
                    {slide.chart && (
                      <div className="chart">
                        <strong>{slide.chart.title}</strong>
                        <small>{slide.chart.unit}</small>
                        {slide.chart.data.map((d, i) => (
                          <div className="bar-row" key={i} title={d.evidence}>
                            <span>{d.label}</span>
                            <div className="bar-track">
                              <i
                                style={{
                                  width: `${(Math.abs(d.value) / Math.max(...slide.chart!.data.map((x) => Math.abs(x.value)), 1)) * 100}%`,
                                }}
                              />
                            </div>
                            <b>{d.value}</b>
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                  <div className="slide-footer">
                    {slide.locked && <span>TEXT LOCKED</span>}
                    <span className="footer-mark">✳</span>
                  </div>
                </article>
              )}
              {slide?.note && <p className="chart-note">{slide.note}</p>}
              <div className="canvas-navigation">
                <button
                  className="subtle"
                  disabled={busy || project.selected === 0}
                  onClick={() =>
                    patch({
                      selected: project.selected - 1,
                      settings: deck.slides[project.selected - 1].settings,
                    })
                  }
                >
                  <ArrowLeft size={16} /> Previous
                </button>
                <span>
                  {(project.selected || 0) + 1} of {deck.slides.length}
                </span>
                <button
                  className="subtle"
                  disabled={busy || project.selected === deck.slides.length - 1}
                  onClick={() =>
                    patch({
                      selected: project.selected + 1,
                      settings: deck.slides[project.selected + 1].settings,
                    })
                  }
                >
                  Next <ArrowRight size={16} />
                </button>
              </div>
              {error && (
                <div className="alert" role="alert">
                  {error}
                </div>
              )}
            </section>
            <aside className="settings">
              <div className="settings-heading">
                <span className="settings-icon">
                  <Sparkles size={18} />
                </span>
                <div>
                  <h2>Refine your content</h2>
                </div>
              </div>
              <div className="section-label">APPLY TO</div>
              <div className="segmented">
                <button
                  disabled={busy}
                  className={scope === "slide" ? "active" : ""}
                  onClick={() => {
                    setScope("slide");
                    if (slide) patch({ settings: slide.settings });
                  }}
                >
                  This slide
                </button>
                <button
                  disabled={busy}
                  className={scope === "deck" ? "active" : ""}
                  onClick={() => setScope("deck")}
                >
                  Entire presentation
                </button>
              </div>
              {controls.map((c) => (
                <div
                  className={
                    "control " +
                    (allLocked && c.key !== "charts" ? "disabled" : "")
                  }
                  key={c.key}
                >
                  <div>
                    <label htmlFor={c.key}>{c.label}</label>
                    <span>{c.levels[project.settings[c.key]]}</span>
                  </div>
                  <input
                    id={c.key}
                    disabled={busy || (allLocked && c.key !== "charts")}
                    type="range"
                    min="0"
                    max="2"
                    step="1"
                    value={project.settings[c.key]}
                    onChange={(e) => {
                      patch({
                        settings: {
                          ...project.settings,
                          [c.key]: Number(e.target.value),
                        },
                      });
                      setPendingRewrite({
                        projectId: project.id,
                        scope,
                        slideId: slide?.id,
                      });
                    }}
                  />
                  <div className="range-labels">
                    <span>{c.levels[0]}</span>
                    <span>{c.levels[2]}</span>
                  </div>
                </div>
              ))}
              <div className="lock-block">
                <label>
                  <LockKeyhole size={17} />
                  <span>Keep exact wording</span>
                  <input
                    disabled={busy}
                    type="checkbox"
                    checked={allLocked}
                    ref={(el) => {
                      if (el) el.indeterminate = someLocked && !allLocked;
                    }}
                    onChange={(e) =>
                      patch({
                        deck: {
                          ...deck,
                          slides: deck.slides.map((s) =>
                            scope === "deck" || s.id === slide?.id
                              ? { ...s, locked: e.target.checked }
                              : s,
                          ),
                        },
                      })
                    }
                  />
                </label>
              </div>
              <button
                className="primary apply"
                disabled={busy}
                onClick={() => run(true)}
              >
                <Sparkles size={16} />
                {busy ? "Updating content…" : "Apply changes"}
              </button>
            </aside>
          </div>
        </main>
      )}
    </>
  );
}

createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
