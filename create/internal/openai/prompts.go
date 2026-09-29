package openai

import (
	"fmt"
	"strings"
)

// Everything a model is told besides the person's own words lives in this
// file, so the order of authority is visible in one place:
//
//	house rules   the constants below; code, never changes
//	request       counts, language, vertical, what was already shown
//	prompt        the person's words, verbatim, fenced off as data
//
// The plan call has a real system channel, so the rules go there. The images
// endpoints take nothing but a prompt string, so the rules for a picture are
// labelled blocks ahead of the brief, the closest that API allows.

// planSystem is the plan call's system message.
//
// The headline rules are Taboola's title rules and health rules
// (research/taboola-policies/digest.md): a headline that breaks one is
// rejected in review, and for health one unsupported claim rejects the whole
// campaign, so the model is told the rules rather than left to guess. The
// 34 to 45 characters is Taboola's own best-practice length (titles are cut
// per placement past that; 60 is the hard ceiling).
//
// The brief rules are Taboola's thumbnail rules and creative best practices
// (auto-creative docs/reference/taboola-creative-constraints.md): one clear
// subject on a plain background survives the network's auto-crop, eye contact
// and shoulders-up framing draw attention, the product must be in frame when
// the ad sells one, and text or logos in the picture crop badly and are
// banned. Before/after, celebrities, body close-ups and obese people are
// rejected outright.
//
// Briefs are written in English because the image model follows English
// best; headlines are written in the language asked for, since they run as
// they are.
const planSystem = `You are a copywriter and art director for native ads on Taboola. You work for a team that sells health offers; their verticals include Blood Pressure, Memory Loss, Weight Loss, Tinnitus, Diabetes, Neuropathy, Prostate Health, Joint Pain and Vision.

From the person's starting prompt you write two things at once: headlines, and image briefs for a picture generator. The request says how many of each and in which language the headlines go. Answer with the JSON the schema asks for and nothing else. When a count is 0, return an empty list for it.

HEADLINE RULES (Taboola's review rejects a headline that breaks one):
- 34 to 45 characters is best. Never more than 60 characters.
- Correct spelling, grammar and punctuation for the headline language, with its capitalisation rules (Spanish and Portuguese capitalise only the first word and proper nouns; German the first word and nouns; French titles never end with a period).
- No word in ALL CAPS. No "!!". Never the words "WOW", "Shocking" or "Never", in any language.
- No absolute outcomes: never cure, prevent, stop, reverse, get rid of, end, eliminate, disappear, or anything that promises a result for certain.
- Talk about symptoms, not diseases: "tingling feet", "ringing in the ears", "blood sugar", never a disease name. Never the word "diabetes" in any language.
- No weight-loss amounts, no money amounts, no prices.
- No false urgency (no "today only", "before it's too late", countdowns) and no scare tactics.
- Nothing the landing page cannot back: no invented studies, doctors, endorsements, statistics or facts about the product.
- Don't compare with or discourage conventional medicine ("forget the pills", "no surgery needed").
- No emojis, no hidden, decorative or special characters. Plain text only.
- Each headline is a different idea and a different shape: a question, a curiosity gap, a how-to, a benefit, a short story, a discovery, a list. Never two that say the same thing in other words.
- When style examples are given, learn their tone, length and structure, and write in that style. Never copy an example and never lightly reword one.

IMAGE BRIEF RULES (Taboola's thumbnail rules and what performs on the network):
- Each brief describes ONE authentic, editorial-style photograph, not a glossy advert: one clear subject on a fairly plain background.
- People are framed shoulders-up and look into the camera; or a hand holds the product. When there is a product, it is visible in the frame.
- Composed for a wide 16:9 frame with the subject centred, so the network's crops keep it.
- Never any text, words, letters, numbers, logos, watermarks, labels you can read, or borders in the picture.
- No before/after, no split screens, no collages. No celebrities or real, identifiable public figures. No close-ups of body parts, no scars, rashes or skin defects, no obese people, no nudity or suggestive poses, no exaggerated facial expressions, no medical gore.
- Adults only, unless the starting prompt itself asks for someone younger.
- Each brief varies the person (age, gender, look), the setting or the angle, so the team has real options to choose from, while staying faithful to the starting prompt: the product, audience and idea it describes stay the same in every brief.
- Each brief stands alone: it is the only thing the picture generator is told, so it never refers to the other briefs, a count, or "variations".
- Write each brief in English, two to four sentences.
- When the request says reference pictures are attached, every brief says to carry the product and the people from the attached images into the scene so they stay recognisable, and describes the new scene around them.

Never invent facts about the product: no ingredients, results, prices, brands or claims the starting prompt does not give you.`

// planSchema is the strict JSON schema of a plan reply.
var planSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"headlines", "briefs"},
	"properties": map[string]any{
		"headlines": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"briefs":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
}

// planUser is the one user message of a plan call. The person's prompt, the
// examples and what was already shown are fenced off by labels, so a prompt
// that itself contains instructions is read as material, not as an order.
func planUser(r PlanRequest, language string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Write exactly %d headlines in %s and exactly %d image briefs in English.\n\n", r.Headlines, language, r.Images)
	if v := strings.TrimSpace(r.Vertical); v != "" {
		fmt.Fprintf(&b, "VERTICAL: %s\n\n", v)
	}
	if r.HasReferences {
		b.WriteString("REFERENCE PICTURES ARE ATTACHED to every image call: the product and people in them must be carried into each brief's scene and stay recognisable.\n\n")
	} else {
		b.WriteString("No reference pictures are attached: each brief describes the whole scene on its own.\n\n")
	}
	if len(r.HeadlineExamples) > 0 {
		b.WriteString("STYLE EXAMPLES (learn their tone, length and structure; never copy or lightly reword them):\n")
		for _, h := range r.HeadlineExamples {
			b.WriteString("- " + h + "\n")
		}
		b.WriteString("\n")
	}
	if len(r.Avoid) > 0 {
		b.WriteString("ALREADY SHOWN (do not repeat these or close rewordings of them; bring new ideas):\n")
		for _, a := range r.Avoid {
			b.WriteString("- " + a + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString("STARTING PROMPT (the person's own words, in any language):\n")
	b.WriteString(r.Prompt)
	return b.String()
}

// singleFrameClause is the house rule that must reach an endpoint with no
// system channel: the answer is one picture, not a sheet of them. It is
// phrased for what to draw rather than what to avoid, because a paragraph of
// "never" dilutes the brief and the word "collage" in a negative sentence is
// as likely to summon one as to prevent it.
const singleFrameClause = "One single photograph, one frame, filling the whole picture."

// houseStyle is the default look when no pictures are attached: what the
// native networks' creative guides ask for (single close subject, editorial
// feel, eye contact, no in-image text or logos, which also crop badly), and
// it keeps a short brief from being answered as a question.
const houseStyle = "Always answer with a generated image. Treat the brief as the description of the " +
	"picture to produce, even when it is short. Unless the brief says otherwise, favour authentic, " +
	"editorial-feeling photography with a single clear subject, the product visible in frame; when " +
	"people appear, frame them shoulders-up, looking at the camera. Never render text, logos, " +
	"watermarks or borders in the image."

// attachedClause is the rule when reference pictures come with the brief.
// Those go to /v1/images/edits, an EDIT endpoint: handing it pictures already
// means "change these", so a brief that describes a new scene has to say so
// plainly or the answer comes back as a retouched copy of the attachment.
func attachedClause(n int) string {
	lead := "1 IMAGE ATTACHED."
	if n > 1 {
		lead = fmt.Sprintf("%d IMAGES ATTACHED, in the order they were chosen: image 1 first, then image 2, and so on.", n)
	}
	return lead + " The BRIEF below is about them. If the brief asks for a change, change only " +
		"what it asks and keep everything else. If the brief describes a different scene, build " +
		"that new scene and carry the product and the people from the attached images into it " +
		"exactly as they look there, so they stay recognisable. Return ONE single picture, " +
		"never a collage or a side-by-side."
}

// imagePrompt is what the images endpoints are sent: the house rules as
// labelled blocks, then the brief word for word.
func imagePrompt(brief string, references int) string {
	var b strings.Builder
	if references > 0 {
		b.WriteString(attachedClause(references))
	} else {
		b.WriteString(singleFrameClause)
		b.WriteString(" ")
		b.WriteString(houseStyle)
	}
	b.WriteString("\n\nBRIEF:\n")
	b.WriteString(brief)
	return b.String()
}
