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
      rewriteRequest = JSON.parse(options.body);
      return { ok: true, json: async () => rewriteRequest.deck };
    }
    return { ok: true, json: async () => structuredClone(result) };
  };
  dom.window.eval(bundle);
  await wait();
  return dom;
}
function button(dom, text) {
  const b = [...dom.window.document.querySelectorAll("button")].find(
    (x) => x.textContent.trim() === text,
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
  await click(dom, "Новый проект");
  await input(dom, "#description", "A presentation draft");
  let saved = dom.window.localStorage.getItem(key);
  assert.equal(JSON.parse(saved)[0].description, "A presentation draft");
  dom.window.close();
  dom = await launch(saved);
  assert.equal(dom.window.document.querySelectorAll(".project-card").length, 1);
  dom.window.document.querySelector(".project-open").click();
  await wait();
  assert.equal(
    dom.window.document.querySelector("#description").value,
    "A presentation draft",
  );
  await click(dom, "Создать содержание");
  await input(dom, ".slide-title", "Manual title");
  dom.window.document.querySelector('input[type="checkbox"]').click();
  await wait();
  assert.ok(dom.window.document.querySelector("#detail").disabled);
  await click(dom, "Этому слайду");
  await click(dom, "Применить изменения");
  assert.equal(rewriteRequest.scope, "slide");
  assert.equal(rewriteRequest.slide_id, "one");
  assert.equal(rewriteRequest.deck.slides[0].title, "Manual title");
  saved = dom.window.localStorage.getItem(key);
  dom.window.close();
  dom = await launch(saved);
  assert.equal(dom.window.document.querySelectorAll(".project-card").length, 1);
  dom.window.document.querySelector(".project-open").click();
  await wait();
  assert.equal(
    dom.window.document.querySelector(".slide-title").value,
    "Manual title",
  );
  assert.ok(
    dom.window.document.querySelector('input[type="checkbox"]').checked,
  );
  assert.ok(dom.window.document.querySelector("#detail").disabled);
  await click(dom, "Все проекты");
  await click(dom, "Новый проект");
  await input(dom, "#description", "Second draft");
  assert.equal(JSON.parse(dom.window.localStorage.getItem(key)).length, 2);
  dom.window.close();
  console.log(
    "PASS: draft autosave, project history, reopen after reload, manual text, lock, rewrite scope, no duplicate projects",
  );
})().catch((error) => {
  console.error(error);
  process.exitCode = 1;
});
