// Warnings from Taboola's rules, read from the Realize help center on
// 2026-09-29 (/mnt/project-files/research/taboola-policies/digest.md) and the
// item limits in its Backstage API reference. They only warn: the person
// always has the final say, so nothing here blocks a download.

// Invisible characters: zero-width spaces and joiners, the word joiner, the
// byte order mark, the soft hyphen, the Mongolian vowel separator and the
// bidi controls. Headlines copied from other ads often carry them, placed
// mid-word to slip past Taboola's keyword review.
export const HIDDEN = /[​-‍⁠﻿­᠎‪-‮⁦-⁩]/g;

// clean removes invisible characters and extra spaces.
export function clean(text) {
  return text.replace(HIDDEN, "").replace(/\s+/g, " ").trim();
}

export function hasHidden(text) {
  return new RegExp(HIDDEN.source).test(text);
}

// Short words that are fine in capitals (Taboola itself asks for "ED").
const ACRONYMS = new Set(["USA", "FDA", "DNA", "CBD", "NYC", "HVAC", "LED", "BMI", "HDL", "LDL", "ADHD", "COVID", "AARP"]);

const RULES = [
  {
    test: (t) => /!!/.test(t),
    say: () => "Mais de um ponto de exclamação: o Taboola recusa.",
  },
  {
    test: (t) => /\b(wow|shocking|never)\b/i.exec(t),
    say: (m) => `“${m[0]}” é sensacionalista para o Taboola.`,
  },
  {
    test: (t) => /\b(cures?|cured|prevents?|stops?|reverses?|reversed|get rid of|end years of|disappears?|eliminates?)\b/i.exec(t),
    say: (m) => `Promete resultado absoluto (“${m[0]}”): o Taboola rejeita.`,
  },
  {
    test: (t) => /diabet/i.test(t),
    say: () => "Cita diabetes: anúncios dessa vertical não podem usar a palavra.",
  },
  {
    test: (t) => /\b(cancer|câncer|alzheimer'?s?|dementia|neuropathy|arthritis|hypertension|glaucoma|cataracts?)\b/i.exec(t),
    say: (m) => `Cita uma doença (“${m[0]}”): o Taboola quer sintomas, não doenças.`,
  },
  {
    test: (t) => /erectile dysfunction|viagra/i.test(t),
    say: () => "Use “ED”, nunca “erectile dysfunction” ou “Viagra”.",
  },
  {
    test: (t) => /(\$\s?\d|R\$\s?\d|\d+\s?(lbs?|pounds|kg|kilos|quilos|dollars|dólares|reais)\b)/i.test(t),
    say: () => "Traz um valor (peso ou dinheiro): o Taboola não aceita.",
  },
  {
    test: (t) => /\p{Extended_Pictographic}/u.test(t),
    say: () => "Tem emoji.",
  },
];

// headlineWarnings lists what in a headline is likely to break Taboola's
// title rules.
export function headlineWarnings(text) {
  const out = [];
  if (hasHidden(text)) out.push("Tem caracteres invisíveis: o Taboola trata como burla da revisão.");
  const t = clean(text);
  if (t.length > 60) out.push(`${t.length} caracteres: passa de 60 (o Taboola recomenda 34 a 45).`);
  const words = t.match(/\p{L}+/gu) || [];
  const loud = words.filter((w) => w.length >= 3 && w === w.toUpperCase() && w !== w.toLowerCase() && !ACRONYMS.has(w));
  if (loud.length) out.push(`Palavra toda em maiúsculas (${loud[0]}): o Taboola recusa.`);
  for (const rule of RULES) {
    const m = rule.test(t);
    if (m) out.push(rule.say(m));
  }
  return out;
}

const TYPES = new Set(["image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp"]);

// imageWarnings checks one picture against Taboola's thumbnail limits:
// at least 600×400 (1000×600 to show everywhere), 2.5 MB at most, 16:9
// preferred.
export function imageWarnings({ width, height, size, type }) {
  const out = [];
  if (type && !TYPES.has(type)) out.push("Formato que o Taboola não aceita: use JPG ou PNG.");
  if (width && height) {
    if (width < 600 || height < 400) out.push(`${width}×${height}: abaixo do mínimo de 600×400.`);
    else if (width < 1000 || height < 600) out.push(`${width}×${height}: menor que 1000×600, fica fora de alguns espaços.`);
    const ratio = width / height;
    if (ratio < 0.95) out.push("Imagem em pé: o Taboola usa 16:9, 4:3 ou 1:1 e vai cortar.");
    else if (ratio > 2.1) out.push("Imagem muito larga: o Taboola vai cortar.");
  }
  if (size > 2.5 * 1024 * 1024) out.push(`${(size / 1048576).toFixed(1).replace(".", ",")} MB: acima de 2,5 MB.`);
  return out;
}

// urlWarnings checks the landing page link put in every ad.
export function urlWarnings(url) {
  const out = [];
  if (!url) return ["Falta o link da página."];
  if (!/^https?:\/\/[^\s/]+\.[^\s]+$/i.test(url)) out.push("O link precisa começar com https:// e não ter espaços.");
  if (/[{}]/.test(url)) {
    out.push("Tem {macros} no link: o Taboola escapa macros no link do anúncio. Ponha os parâmetros do RedTrack no Tracking Code da campanha.");
  }
  return out;
}

// Markers generators leave in a file: OpenAI's pictures carry a C2PA
// manifest naming it. Finding one means the picture is likely AI-made; not
// finding one proves nothing.
const AI_MARKERS = ["c2pa", "OpenAI", "gpt-image", "DALL", "Midjourney", "Gemini", "Imagen", "firefly"];

export function looksAIMade(bytes) {
  const n = Math.min(bytes.length, 256 * 1024);
  let text = "";
  for (let i = 0; i < n; i++) text += String.fromCharCode(bytes[i] < 128 ? bytes[i] : 32);
  if (bytes.length > n) {
    for (let i = Math.max(n, bytes.length - 64 * 1024); i < bytes.length; i++) text += String.fromCharCode(bytes[i] < 128 ? bytes[i] : 32);
  }
  return AI_MARKERS.some((m) => text.includes(m));
}
