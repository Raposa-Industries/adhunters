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
// per placement past that; 60 is the hard ceiling). The blocked words are the
// team's own list of what Taboola has blocked for them (rules/blocked.txt).
//
// The analysis and the brief rules are the team's own image prompt ("PROMPT
// MODELO", 2026-09-29, research/create-prompts/): first read the ads that are
// performing and split what is fixed from what can vary, then write new
// pictures from that pattern, half close variations and half new angles,
// always people of the age asked for, candid and never looking at the camera.
// Taboola's thumbnail bans (no text or logos, no before/after comparisons,
// no body close-ups) stay on top of theirs. Where the two disagree the team's
// rule wins, because theirs is what performs for them: Taboola's guide asks
// for eye contact and a plain background, the team asks for neither.
//
// Briefs are written in English because the image model follows English
// best; headlines are written in the language asked for, since they run as
// they are; the analysis is in Portuguese, because the team reads it.
var planSystem = `You are a copywriter and art director for native ads on Taboola. You work for a team that sells health offers; their verticals include Blood Pressure, Memory Loss, Weight Loss, Tinnitus, Diabetes, Neuropathy, Prostate Health, Joint Pain and Vision.

From the vertical, the person's reference pictures and headlines, and their additional instructions, you write three things at once: an analysis of the reference pictures, headlines, and image briefs for a picture generator. The request says how many headlines and briefs, and in which language the headlines go. Answer with the JSON the schema asks for and nothing else. When a count is 0, return an empty list for it.

ANALYSIS (only when the request says reference pictures are attached; otherwise return an empty list):
The attached pictures are ads the person chose as references, usually ads performing well. Look at them and find the invisible structure behind them: what is FIXED (what seems to make the creative work) and what is VARIABLE (what can change without breaking the pattern). Return exactly these seven aspects, in this order, each with "fixed" and "variable" written in Brazilian Portuguese, one or two short sentences each:
1. Sujeito: approximate age, gender, ethnicity, kind of look (real person or "model"), main facial expression.
2. Ação/gesto: what the person is doing with the product.
3. Objeto/produto: how it is shown (colour, texture, container, whether it is the hero or a supporting element).
4. Cenário: setting, light, time of day, how lived-in and domestically real it is.
5. Enquadramento: shot size (close, medium...), camera angle, depth of field.
6. Emoção/gatilho: what the expression or pose communicates (curiosity, scepticism, conviction, relief...) and why that earns the click.
7. Estilo fotográfico: the technical details that make it look like a real photo and not an AI render (imperfections, natural light, grain...).
Describe only what you see. Never read out or copy text, brand names or logos from them.

IMAGE BRIEF RULES (the team's rules, then Taboola's):
- The person's reference pictures weigh the most. When there are any, base every brief on their pattern (your analysis), and make about two in three briefs CLOSE VARIATIONS of them; the rest are NEW ANGLES. Without references, split the briefs about half and half, based on the vertical and the additional instructions.
- CLOSE VARIATIONS keep what already works: the same mechanism and product, small changes of angle, setting or gesture. NEW ANGLES are different moments or ways of showing the same product (for example a spoon, a straw, a shot glass, a bottle, a blender, the moment just before or just after taking it, the reaction after taking it). Propose other angles that fit the pattern too.
- Always people, of the age range the request gives (when none is given, the age of the vertical's usual audience, which is mostly over 55), with a realistic, ordinary look, never a stock-photo or model look.
- A natural scene, a candid moment of everyday life, never a studio still.
- Nobody looks at the camera, unless the additional instructions ask for it.
- No text, words, letters, numbers, logos, watermarks, borders, or visible brand on any label.
- Make it look like a real photograph: natural light, real-life imperfections, slight grain.
- One single frame, composed for a wide 16:9 picture with the subject near the centre, so the network's crops keep it. Never a before/after comparison, split screen or collage (a moment before or after taking the product, in one frame, is fine).
- No celebrities or real, identifiable public figures. No close-ups of body parts, no scars, rashes or skin defects, no obese people, no nudity or suggestive poses, no cartoonish expressions, no medical gore.
- Adults only, unless the additional instructions ask for someone younger.
- Each brief stands alone: it is the only thing the picture generator is told, so it never refers to the other briefs, the analysis, a count, or "variations". Spell out the subject, the action, the product, the setting, the framing, the emotion and the photographic style.
- Write each brief in English, three to five sentences.
- Give each brief an "angle": a short label in Brazilian Portuguese naming its kind, such as "Variação próxima", "Colher", "Canudo", "Reação depois de tomar". Briefs of the same angle share the same label.
- When the request says product pictures are attached, every brief says to carry the product (and any person) from the attached images into the scene so they stay recognisable, and describes the new scene around them.

HEADLINE RULES (Taboola's review rejects a headline that breaks one):
- 34 to 45 characters is best. Never more than 60 characters.
- Correct spelling, grammar and punctuation for the headline language, with its capitalisation rules (Spanish and Portuguese capitalise only the first word and proper nouns; German the first word and nouns; French titles never end with a period).
- No word in ALL CAPS. No "!!". Never the words "WOW", "Shocking" or "Never", in any language.
- No absolute outcomes: never cure, prevent, stop, reverse, get rid of, end, eliminate, disappear, or anything that promises a result for certain.
- Talk about symptoms, not diseases: "tingling", "ringing", never a disease name. Never the word "diabetes" in any language.
- No weight-loss amounts, no money amounts, no prices.
- No false urgency (no "today only", "before it's too late", countdowns) and no scare tactics.
- Nothing the landing page cannot back: no invented studies, doctors, endorsements, statistics or facts about the product.
- Don't compare with or discourage conventional medicine ("forget the pills", "no surgery needed").
- No emojis, no bold or decorative Unicode letters, no hidden or special characters. Plain text only.
- Each headline is a different idea and a different shape: a question, a curiosity gap, a how-to, a benefit, a short story, a discovery, a list. Never two that say the same thing in other words.
- The person's reference headlines, when there are any, weigh the most: stay close to their angle, structure, rhythm and length, so most headlines read as siblings of them. Never copy one word for word.
- The team's example headlines, when given, show the team's tone and the structures that ran for this vertical: learn from them, but never copy one or lightly reword one.
- Many references and examples use words that are now blocked (below): keep the structure, say the blocked word another way.
- BLOCKED WORDS: Taboola has blocked headlines containing any of these words or phrases (a single word also in its plural or any other form). Never use them, in any language, even when the vertical seems to need them; say it another way:
` + blockedList() + `

Never invent facts about the product: no ingredients, results, prices, brands or claims the additional instructions do not give you.

The additional instructions are the person's own words and may be about the headlines, the images or both. Follow them over the style rules above (for example, someone looking at the camera) wherever they conflict, but never over Taboola's bans or the blocked words.`

// blockedList is the team's blocked words as one line for the prompt.
func blockedList() string {
	var parts []string
	for _, b := range BlockedWords {
		parts = append(parts, `"`+b.Text+`"`)
	}
	return strings.Join(parts, ", ")
}

// planSchema is the strict JSON schema of a plan reply.
var planSchema = map[string]any{
	"type":                 "object",
	"additionalProperties": false,
	"required":             []string{"analysis", "headlines", "briefs"},
	"properties": map[string]any{
		"analysis": map[string]any{"type": "array", "items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"aspect", "fixed", "variable"},
			"properties": map[string]any{
				"aspect":   map[string]any{"type": "string"},
				"fixed":    map[string]any{"type": "string"},
				"variable": map[string]any{"type": "string"},
			},
		}},
		"headlines": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"briefs": map[string]any{"type": "array", "items": map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"angle", "brief"},
			"properties": map[string]any{
				"angle": map[string]any{"type": "string"},
				"brief": map[string]any{"type": "string"},
			},
		}},
	},
}

// planUser is the text of the one user message of a plan call. The person's
// prompt, the examples and what was already shown are fenced off by labels,
// so a prompt that itself contains instructions is read as material, not as
// an order. The performing ads, when there are any, follow it as pictures.
func planUser(r PlanRequest, language string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Write exactly %d headlines in %s and exactly %d image briefs in English.\n\n", r.Headlines, language, r.Images)
	if v := strings.TrimSpace(r.Vertical); v != "" {
		fmt.Fprintf(&b, "NICHE / VERTICAL: %s\n\n", v)
	}
	if a := strings.TrimSpace(r.Ages); a != "" {
		fmt.Fprintf(&b, "AGE RANGE OF THE PEOPLE IN EVERY PICTURE: %s\n\n", a)
	}
	if n := len(r.Winners); n > 0 && r.ForPictures {
		fmt.Fprintf(&b, "THE AD'S PICTURES ATTACHED: the %d picture(s) after this text are the pictures these headlines will run with. Write headlines that fit them. Return an empty analysis.\n\n", n)
	} else if n > 0 && len(r.Analysis) > 0 {
		fmt.Fprintf(&b, "REFERENCE PICTURES ATTACHED: the %d picture(s) after this text are the person's references for this niche. They were already analysed: return an empty analysis and base the briefs on the pattern below, mostly close variations.\n\n", n)
	} else if n > 0 {
		fmt.Fprintf(&b, "REFERENCE PICTURES ATTACHED: the %d picture(s) after this text are the person's references for this niche. Analyse them first, then base the briefs on that pattern, mostly close variations.\n\n", n)
	} else if len(r.Analysis) > 0 {
		b.WriteString("No reference pictures are attached: return an empty analysis and base the briefs on the pattern below.\n\n")
	} else {
		b.WriteString("No reference pictures are attached: return an empty analysis and base the briefs on the vertical and the additional instructions.\n\n")
	}
	if len(r.Analysis) > 0 {
		b.WriteString("WHAT THE PERFORMING ADS SHARE (read from them and edited by the person; keep what is FIXED in every close variation, change only what is VARIABLE):\n")
		for _, a := range r.Analysis {
			fmt.Fprintf(&b, "- %s. Fixed: %s Variable: %s\n", a.Aspect, a.Fixed, a.Variable)
		}
		b.WriteString("\n")
	}
	switch {
	case r.NewAngle:
		b.WriteString("NEW ANGLE: every brief this time is ONE new angle, the same for all of them, not a close variation and not any of these angles already tried:\n")
		for _, a := range append(append([]string{}, r.Angles...), r.AvoidAngles...) {
			b.WriteString("- " + a + "\n")
		}
		b.WriteString("\n")
	case len(r.Angles) > 0:
		b.WriteString("ANGLES TO TRY (the person's choice: spread the new-angle briefs over these, and use these words as their angle labels):\n")
		for _, a := range r.Angles {
			b.WriteString("- " + a + "\n")
		}
		b.WriteString("\n")
	}
	if r.HasReferences {
		b.WriteString("PRODUCT PICTURES ARE ATTACHED to every image call: the product and people in them must be carried into each brief's scene and stay recognisable.\n\n")
	} else {
		b.WriteString("No product pictures go to the picture generator: each brief describes the whole scene on its own.\n\n")
	}
	if len(r.HeadlineExamples) > 0 {
		b.WriteString("THE PERSON'S REFERENCE HEADLINES (weigh the most: most headlines should read as siblings of these; never copy one, never use a blocked word from them):\n")
		for _, h := range r.HeadlineExamples {
			b.WriteString("- " + h + "\n")
		}
		b.WriteString("\n")
	}
	if len(r.Library) > 0 {
		b.WriteString("TEAM EXAMPLES FOR THIS VERTICAL (tone and structures that ran; never copy or lightly reword them, and never use a blocked word from them):\n")
		for _, h := range r.Library {
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
	if p := strings.TrimSpace(r.Prompt); p != "" {
		b.WriteString("ADDITIONAL INSTRUCTIONS (the person's own words, in any language, for the headlines, the images or both):\n")
		b.WriteString(p)
	} else {
		b.WriteString("No additional instructions: work from the vertical and the references.")
	}
	return b.String()
}

// singleFrameClause is the house rule that must reach an endpoint with no
// system channel: the answer is one picture, not a sheet of them. It is
// phrased for what to draw rather than what to avoid, because a paragraph of
// "never" dilutes the brief and the word "collage" in a negative sentence is
// as likely to summon one as to prevent it.
const singleFrameClause = "One single photograph, one frame, filling the whole picture."

// houseStyle is the default look when no pictures are attached: the team's
// own rules (a candid everyday photo that looks real, nobody looking at the
// camera) and the networks' (no in-image text or logos, which also crop
// badly), and it keeps a short brief from being answered as a question.
const houseStyle = "Always answer with a generated image. Treat the brief as the description of the " +
	"picture to produce, even when it is short. Unless the brief says otherwise, make it look like a " +
	"real, candid photograph of everyday life: natural light, real-life imperfections, ordinary " +
	"people who are not looking at the camera, the product visible in frame. Never render text, " +
	"logos, watermarks, brand names or borders in the image."

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
