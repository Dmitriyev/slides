const assert = require("node:assert/strict");
const fs = require("node:fs");
const { JSDOM } = require("jsdom");
const path = require("node:path");
const root = path.join(__dirname, "..");
const index = fs.readFileSync(path.join(root, "dist/index.html"), "utf8");
const jsPath = index.match(/src="([^"]+\.js)"/)[1];
const bundle = fs.readFileSync(path.join(root, "dist", jsPath), "utf8");
const key = "slide-tuner.projects.v1";
const wait = () => new Promise((resolve) => setTimeout(resolve, 30));
const settings = { detail: 1, complexity: 1, tone: 1, charts: 1 };
const result = {
  title: "Project test",
  source: "Source text",
  slides: [
    {
      id: "one",
      title: "First slide",
      paragraphs: ["Original paragraph"],
      chart: null,
      note: "",
      locked: false,
      settings,
    },
    {
      id: "two",
      title: "Second slide",
      paragraphs: ["Unchanged"],
      chart: null,
      note: "",
      locked: false,
      settings,
    },
  ],
};
let rewriteRequest;
let rewriteCount = 0;
let chatRequest;
let failChat = false;
let rewriteHook;
let active = 0;
let maxActive = 0;
async function launch(saved) {
  const dom = new JSDOM(
    '<html><head></head><body><div id="root"></div></body></html>',
    {
      url: "http://localhost:5173",
      runScripts: "outside-only",
      pretendToBeVisual: true,
    },
  );
  dom.window.structuredClone = structuredClone;
  if (saved) dom.window.localStorage.setItem(key, saved);
  dom.window.fetch = async (url, options) => {
    if (url.endsWith("rewrite")) {
      rewriteCount++;
      rewriteRequest = JSON.parse(options.body);
      const request = JSON.parse(options.body);
      active++;
      maxActive = Math.max(maxActive, active);
      try {
        if (rewriteHook) await rewriteHook(request);
        await new Promise((resolve) => setTimeout(resolve, 5));
        const deck = structuredClone(request.deck);
        deck.slides = deck.slides.map((s) =>
          request.scope === "deck" || s.id === request.slide_id
            ? {
                ...s,
                title: s.locked
                  ? s.title
                  : `Variant ${request.settings.detail}/${request.settings.complexity}/${request.settings.tone}/${request.settings.charts}`,
                settings: s.locked
                  ? { ...s.settings, charts: request.settings.charts }
                  : request.settings,
              }
            : s,
        );
        return { ok: true, json: async () => deck };
      } finally {
        active--;
      }
    }
    if (url.endsWith("chat")) {
      chatRequest = JSON.parse(options.body);
      if (failChat)
        return {
          ok: false,
          status: 502,
          json: async () => ({ detail: "Provider unavailable" }),
        };
      const deck = structuredClone(chatRequest.deck);
      deck.slides = deck.slides.map((s) =>
        s.id === chatRequest.slide_id
          ? { ...s, paragraphs: ["Edited through chat"] }
          : s,
      );
      return {
        ok: true,
        json: async () => ({
          deck,
          changed: true,
          message: "Updated the selected slide",
        }),
      };
    }
    return { ok: true, json: async () => structuredClone(result) };
  };
  dom.window.eval(bundle);
  await wait();
  return dom;
}
function button(dom, text) {
  const b = [...dom.window.document.querySelectorAll("button")].find((x) =>
    x.textContent.replace(/\s+/g, " ").trim().includes(text),
  );
  assert.ok(b, `Button ${text} must exist`);
  return b;
}
async function click(dom, text) {
  button(dom, text).click();
  await wait();
}
async function input(dom, selector, value) {
  const el = dom.window.document.querySelector(selector);
  assert.ok(el, selector);
  const prototype =
    el.tagName === "TEXTAREA"
      ? dom.window.HTMLTextAreaElement.prototype
      : dom.window.HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(prototype, "value").set.call(el, value);
  el.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
  await wait();
}
(async () => {
  let dom = await launch();
  assert.equal(dom.window.document.querySelector('input[type="number"]'), null);
  assert.ok(dom.window.document.querySelector(".chat-pane"));
  assert.ok(dom.window.document.querySelector(".upload-pane"));
  await input(dom, "#description", "A presentation draft");
  let saved = dom.window.localStorage.getItem(key);
  assert.equal(JSON.parse(saved)[0].description, "A presentation draft");
  dom.window.close();
  dom = await launch(saved);
  assert.equal(
    dom.window.document.querySelector("#description").value,
    "A presentation draft",
  );
  await click(dom, "My projects");
  assert.equal(dom.window.document.querySelectorAll(".project-card").length, 1);
  dom.window.document.querySelector(".project-open").click();
  await wait();
  assert.equal(
    dom.window.document.querySelector("#description").value,
    "A presentation draft",
  );
  await click(dom, "Generate content");
  const state = () => JSON.parse(dom.window.localStorage.getItem(key))[0];
  async function until(predicate) {
    for (let n = 0; n < 150; n++) {
      if (predicate()) return;
      await wait();
    }
    assert.fail("Timed out waiting for state");
  }
  await until(() => state().service.variants.length === 9);
  assert.equal(
    rewriteCount,
    8,
    "The initial version plus eight alternatives makes nine",
  );
  assert.ok(maxActive <= 2, "Background work must have bounded concurrency");
  assert.equal(
    dom.window.document.querySelector(".slide-title").value,
    "First slide",
    "Prefetch must not replace visible content",
  );
  const countBeforeHit = rewriteCount;
  await input(dom, "#detail", "2");
  assert.equal(
    dom.window.document.querySelector(".slide-title").value,
    "Variant 2/1/1/1",
    "A cached option must apply immediately",
  );
  assert.equal(
    rewriteCount,
    countBeforeHit,
    "Cached switching must not make a foreground request",
  );
  await input(dom, "#detail", "1");
  assert.equal(
    dom.window.document.querySelector(".slide-title").value,
    "First slide",
    "Returning to the base must restore the exact original text",
  );
  await click(dom, "This slide");
  await until(
    () =>
      state().service.variants.filter((v) => v.scope === "slide").length === 9,
  );
  const other = structuredClone(state().deck.slides[1]);
  await input(dom, "#complexity", "0");
  assert.equal(
    dom.window.document.querySelector(".slide-title").value,
    "Variant 1/0/1/1",
  );
  assert.deepEqual(
    state().deck.slides[1],
    other,
    "Cached slide variants preserve other slides",
  );
  await input(dom, "#complexity", "1");
  await input(dom, "#detail", "0");
  await input(dom, "#complexity", "2");
  await click(dom, "Next");
  await wait();
  assert.equal(state().selected, 1);
  assert.deepEqual(
    state().deck.slides[1],
    other,
    "Navigation cancels a pending slider rewrite",
  );
  await click(dom, "Previous");
  await input(dom, ".slide-title", "Manual title");
  assert.equal(
    state().service.variants.length,
    0,
    "Manual edits invalidate previous alternatives",
  );
  const revision = state().service.revision;
  dom.window.document.querySelector('input[type="checkbox"]').click();
  await wait();
  assert.ok(dom.window.document.querySelector("#detail").disabled);
  assert.notEqual(
    state().service.revision,
    revision,
    "Lock changes invalidate alternatives too",
  );
  // Unlock to exercise arbitrary chat edits after alternatives are prepared.
  dom.window.document.querySelector('input[type="checkbox"]').click();
  await until(() => state().service.variants.length === 9);
  const beforeChat = state().service.revision;
  await input(dom, "#chat-message", "Rewrite the selected slide for investors");
  await click(dom, "Send");
  assert.equal(chatRequest.scope, "slide");
  assert.equal(chatRequest.slide_id, "one");
  assert.equal(chatRequest.deck.slides[0].title, "Manual title");
  assert.equal(
    chatRequest.alternatives.length,
    9,
    "The chat agent must see alternative texts",
  );
  assert.ok(
    chatRequest.messages.some((m) => m.text === "A presentation draft"),
  );
  assert.equal(state().deck.slides[0].paragraphs[0], "Edited through chat");
  assert.notEqual(state().service.revision, beforeChat);
  await click(dom, "Undo generation");
  assert.equal(state().deck.slides[0].paragraphs[0], "Original paragraph", "Undo must restore the pre-chat version");
  assert.deepEqual(state().settings, state().deck.slides[state().selected].settings, "Undo restores settings with content");
  assert.equal(
    state().service.messages.at(-1).text,
    "Updated the selected slide",
  );
  const preserved = structuredClone(state().deck);
  failChat = true;
  await input(dom, "#chat-message", "Another edit");
  await click(dom, "Send");
  assert.deepEqual(
    state().deck,
    preserved,
    "Failed chat requests preserve the deck",
  );
  assert.equal(
    dom.window.document.querySelector("#chat-message").value,
    "Another edit",
  );
  failChat = false;
  dom.window.document.querySelector('input[type="checkbox"]').click();
  await wait();
  saved = dom.window.localStorage.getItem(key);
  assert.equal(JSON.parse(saved)[0].deck.slides[0].locked, true);
  dom.window.close();
  dom = await launch(saved);
  await click(dom, "This slide");
  assert.equal(
    dom.window.document.querySelector(".slide-title").value,
    "Manual title",
  );
  assert.ok(
    dom.window.document.querySelector('input[type="checkbox"]').checked,
  );
  assert.ok(dom.window.document.querySelector("#detail").disabled);
  assert.ok(
    dom.window.document
      .querySelector('[role="log"]')
      .textContent.includes("Updated the selected slide"),
    "Chat history survives reload",
  );
  await click(dom, "All projects");
  await click(dom, "New project");
  await input(dom, "#description", "Second draft");
  assert.equal(JSON.parse(dom.window.localStorage.getItem(key)).length, 2);
  dom.window.close();
  // A late background response must never restore pre-edit alternatives.
  rewriteCount = 0;
  let release;
  const gate = new Promise((resolve) => {
    release = resolve;
  });
  rewriteHook = () => gate;
  const legacy = [
    {
      id: "legacy",
      name: "Legacy",
      updatedAt: new Date().toISOString(),
      description: "Brief",
      count: 2,
      settings,
      deck: result,
      selected: 0,
    },
  ];
  dom = await launch(JSON.stringify(legacy));
  await until(() => rewriteCount >= 1);
  await input(dom, ".slide-title", "New revision");
  release();
  rewriteHook = undefined;
  await new Promise((resolve) => setTimeout(resolve, 150));
  assert.equal(
    dom.window.document.querySelector(".slide-title").value,
    "New revision",
  );
  assert.equal(
    state().service.variants.length,
    0,
    "Late responses from previous revisions must be discarded",
  );
  await until(() => state().service.variants.length === 9);
  assert.equal(maxActive <= 2, true);
  dom.window.close();
  console.log(
    "PASS: migration, autosave, nine alternatives, instant cache hits, bounded concurrency, scope, stale responses, manual edits, chat context, chat failure recovery, reload and history",
  );
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
