export type Settings = {
  detail: number;
  complexity: number;
  tone: number;
  charts: number;
};
export type Chart = {
  title: string;
  unit: string;
  data: { label: string; value: number; evidence: string }[];
};
export type Slide = {
  id: string;
  title: string;
  paragraphs: string[];
  chart: Chart | null;
  note: string;
  locked: boolean;
  settings: Settings;
};
export type Content = { title: string; slides: Slide[] };
export type Deck = Content & { source: string };
export type ChatMessage = { role: "user" | "assistant"; text: string };
export type Variant = {
  key: string;
  scope: "slide" | "deck";
  slide_id?: string;
  settings: Settings;
  content: Content;
};
export type Service = {
  schemaVersion: 2;
  revision: string;
  base: Content | null;
  variants: Variant[];
  messages: ChatMessage[];
  history: Content[];
};
export type Project = {
  id: string;
  name: string;
  updatedAt: string;
  description: string;
  count: number;
  settings: Settings;
  deck: Deck | null;
  selected: number;
  service: Service;
};
export function contentOf(deck: Content): Content {
  return structuredClone({ title: deck.title, slides: deck.slides });
}
export function newService(deck: Content | null = null): Service {
  return {
    schemaVersion: 2,
    revision: crypto.randomUUID(),
    base: deck ? contentOf(deck) : null,
    variants: [],
    messages: [],
    history: [],
  };
}
export function variantKey(
  scope: "slide" | "deck",
  slideId: string | undefined,
  s: Settings,
) {
  return JSON.stringify([
    scope,
    scope === "slide" ? slideId : null,
    s.detail,
    s.complexity,
    s.tone,
    s.charts,
  ]);
}
// The current combination plus two alternatives per control: nine, not 81.
export function neighborhood(settings: Settings): Settings[] {
  const result = [{ ...settings }];
  for (const key of ["detail", "complexity", "tone", "charts"] as const)
    for (let value = 0; value < 3; value++)
      if (value !== settings[key]) result.push({ ...settings, [key]: value });
  return result;
}
export function applyVariant(deck: Deck, variant: Variant): Deck {
  return variant.scope === "deck"
    ? { ...contentOf(variant.content), source: deck.source }
    : {
        ...deck,
        slides: deck.slides.map(
          (s) => variant.content.slides.find((v) => v.id === s.id) ?? s,
        ),
      };
}
