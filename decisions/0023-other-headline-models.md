# 0023 · Other text models may write headlines; pictures stay OpenAI's

**Decided:** 2 Oct 2026, from the team's Create feedback (the leader asked
to try xAI's Grok, DeepSeek and Moonshot's Kimi for headlines). Changes
"OpenAI is the only generator" of [0015](0015-create-briefs.md) and
[0019](0019-create-is-a-chat.md) for headlines only.

- **OpenAI stays the default**, and the only generator for pictures (and for
  the briefs of several pictures from words alone). Nothing changes for
  someone who sets nothing.
- **Grok, DeepSeek and Kimi are opt-in, for headlines.** Each is off until
  its key is set in Create's environment (`CREATE_GROK_API_KEY`,
  `CREATE_DEEPSEEK_API_KEY`, `CREATE_KIMI_API_KEY`, each with a base URL and
  model setting). With at least one on, the composer shows a small picker
  for the headline model; the turn remembers which one wrote its headlines.
- **Same rules for all.** They are called through the same OpenAI-compatible
  chat-completions request, with the same system prompt (Taboola's rules,
  the blocked words, English headlines) and the same headline memory. Every
  reply is kept on disk as it came, before it is read (raw first, 0003), and
  every headline gets the same warnings. A model that answers with something
  unreadable fails that turn's headlines, never the pictures.
- **No real calls in tests**: they run against fakes.

**Why:** headlines are cheap text, the team wants to compare tones, and the
three speak OpenAI's chat-completions dialect, so one client serves them.
Pictures need OpenAI's image endpoints and the team's proven prompts.

**Not decided:** which one wins. Their costs are counted under their own
provider name in `/metrics`, so the team can compare.
