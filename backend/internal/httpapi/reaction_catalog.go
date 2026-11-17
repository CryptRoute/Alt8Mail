package httpapi

type reactionEmojiItem struct {
	Emoji    string `json:"emoji"`
	Label    string `json:"label"`
	Keywords string `json:"keywords"`
	Semantic string `json:"semantic"`
}

type reactionEmojiCategory struct {
	Name   string              `json:"name"`
	Emojis []reactionEmojiItem `json:"emojis"`
}

// reactionEmojiCategories is the shared catalog used by the webmail picker,
// reaction rules and server-side validation. Keep the small quick set below in
// the first category so one tap remains available for the most common replies.
var reactionEmojiCategories = []reactionEmojiCategory{
	{Name: "Smileys & emotion", Emojis: []reactionEmojiItem{
		{"😀", "Grinning", "happy smile", "happiness"},
		{"😃", "Big smile", "happy joy", "happiness"},
		{"😄", "Smiling eyes", "happy laugh", "happiness"},
		{"😁", "Beaming", "grin happy", "happiness"},
		{"😆", "Laughing", "laugh funny", "humour"},
		{"😅", "Relieved laugh", "sweat relief", "humour"},
		{"😂", "Tears of joy", "laugh funny lol", "humour"},
		{"🤣", "Rolling laugh", "laugh lol", "humour"},
		{"😊", "Warm smile", "smile warm", "appreciation"},
		{"😇", "Angel", "halo innocent", "appreciation"},
		{"🙂", "Slight smile", "smile", "acknowledgement"},
		{"😉", "Wink", "wink playful", "playfulness"},
		{"😍", "Heart eyes", "love adore", "appreciation"},
		{"🥰", "Hearts", "love warm", "appreciation"},
		{"😘", "Kiss", "kiss love", "appreciation"},
		{"😋", "Yum", "food tasty", "playfulness"},
		{"😎", "Cool", "sunglasses cool", "confidence"},
		{"🤩", "Star-struck", "wow excited", "excitement"},
		{"🥳", "Partying", "party celebrate", "celebration"},
		{"😏", "Smirk", "smirk playful", "playfulness"},
		{"😢", "Sad", "sad tear", "sympathy"},
		{"😭", "Crying", "sad cry", "sympathy"},
		{"😤", "Frustrated", "steam annoyed", "frustration"},
		{"😡", "Angry", "angry mad", "frustration"},
		{"🤯", "Mind blown", "wow shock", "surprise"},
		{"😴", "Sleepy", "tired sleep", "acknowledgement"},
		{"🤔", "Thinking", "think question", "needs thought"},
		{"🤗", "Hug", "hug warm", "appreciation"},
		{"😬", "Grimace", "awkward nervous", "awkwardness"},
		{"🙃", "Upside down", "silly playful", "playfulness"},
		{"🫠", "Melting", "melting overwhelmed", "humour"},
		{"🫡", "Salute", "salute respect", "respect"},
	}},
	{Name: "Gestures & people", Emojis: []reactionEmojiItem{
		{"👍", "Thumbs up", "approve yes good", "acknowledgement"},
		{"👎", "Thumbs down", "no dislike", "disagreement"},
		{"👏", "Applause", "clap bravo", "appreciation"},
		{"🙌", "Raised hands", "hooray celebrate", "celebration"},
		{"🙏", "Folded hands", "thanks please gratitude", "gratitude"},
		{"🤝", "Handshake", "deal agreement", "agreement"},
		{"✌️", "Victory", "peace victory", "celebration"},
		{"🤞", "Fingers crossed", "hope luck", "hope"},
		{"👌", "OK hand", "perfect okay", "approval"},
		{"🫶", "Heart hands", "love appreciate", "appreciation"},
		{"💪", "Flexed biceps", "strong power", "encouragement"},
		{"👀", "Eyes", "attention look", "attention"},
		{"☝️", "Pointing up", "point one", "attention"},
		{"👇", "Pointing down", "point below", "attention"},
		{"👆", "Pointing up", "point above", "attention"},
		{"🖐️", "Hand", "wave hello", "acknowledgement"},
	}},
	{Name: "Hearts & appreciation", Emojis: []reactionEmojiItem{
		{"❤️", "Red heart", "love heart", "appreciation"},
		{"🧡", "Orange heart", "love warm", "appreciation"},
		{"💛", "Yellow heart", "love friendship", "appreciation"},
		{"💚", "Green heart", "love care", "appreciation"},
		{"💙", "Blue heart", "love calm", "appreciation"},
		{"💜", "Purple heart", "love care", "appreciation"},
		{"🖤", "Black heart", "love dark", "appreciation"},
		{"🤍", "White heart", "love pure", "appreciation"},
		{"💔", "Broken heart", "sad heartbreak", "sympathy"},
		{"💯", "Hundred points", "perfect strong agreement", "strong agreement"},
	}},
	{Name: "Celebration & success", Emojis: []reactionEmojiItem{
		{"🎉", "Party popper", "celebrate congrats", "celebration"},
		{"🎊", "Confetti", "celebrate party", "celebration"},
		{"🎂", "Cake", "birthday celebrate", "celebration"},
		{"🎁", "Gift", "present thanks", "appreciation"},
		{"🏆", "Trophy", "win success", "celebration"},
		{"🥇", "First place", "gold win", "celebration"},
		{"🚀", "Rocket", "launch ship fast", "excitement"},
		{"✨", "Sparkles", "magic shine", "excitement"},
		{"⭐", "Star", "favourite star", "appreciation"},
		{"🔥", "Fire", "hot great", "excitement"},
		{"💥", "Boom", "impact explosion", "excitement"},
		{"🌟", "Glowing star", "shine great", "appreciation"},
		{"🎯", "Bullseye", "target accurate", "approval"},
		{"📣", "Megaphone", "announce shout", "attention"},
		{"🔔", "Bell", "notify reminder", "attention"},
	}},
	{Name: "Work & focus", Emojis: []reactionEmojiItem{
		{"✅", "Check", "done approve", "approval"},
		{"☑️", "Checked box", "done complete", "approval"},
		{"📌", "Pin", "pin note reminder", "attention"},
		{"📎", "Paperclip", "attach file", "acknowledgement"},
		{"📅", "Calendar", "date schedule", "acknowledgement"},
		{"⏰", "Alarm", "time reminder", "attention"},
		{"💡", "Idea", "idea insight", "needs thought"},
		{"📈", "Up chart", "growth progress", "celebration"},
		{"📉", "Down chart", "decline drop", "attention"},
		{"💼", "Briefcase", "work business", "acknowledgement"},
		{"🧾", "Receipt", "invoice expense", "acknowledgement"},
		{"🔒", "Locked", "secure private", "acknowledgement"},
		{"🔓", "Unlocked", "open access", "acknowledgement"},
		{"⚠️", "Warning", "caution alert", "attention"},
		{"❗", "Exclamation", "important alert", "attention"},
		{"❓", "Question", "question help", "needs thought"},
		{"📝", "Memo", "note write", "acknowledgement"},
		{"📊", "Bar chart", "report data", "acknowledgement"},
	}},
	{Name: "Nature & food", Emojis: []reactionEmojiItem{
		{"🌈", "Rainbow", "colour hope", "happiness"},
		{"☀️", "Sun", "sunny bright", "happiness"},
		{"🌸", "Blossom", "flower spring", "appreciation"},
		{"🌻", "Sunflower", "flower happy", "appreciation"},
		{"🍀", "Clover", "luck lucky", "hope"},
		{"🍕", "Pizza", "food lunch", "playfulness"},
		{"🍔", "Burger", "food lunch", "playfulness"},
		{"🍰", "Cake", "dessert sweet", "celebration"},
		{"☕", "Coffee", "coffee break", "acknowledgement"},
		{"🍻", "Cheers", "beer toast", "celebration"},
		{"🌮", "Taco", "food lunch", "playfulness"},
		{"🍩", "Doughnut", "sweet snack", "playfulness"},
		{"🍎", "Apple", "fruit healthy", "acknowledgement"},
		{"🌙", "Moon", "night evening", "acknowledgement"},
		{"🌊", "Wave", "ocean calm", "acknowledgement"},
	}},
	{Name: "Animals & travel", Emojis: []reactionEmojiItem{
		{"🐶", "Dog", "puppy pet", "happiness"},
		{"🐱", "Cat", "kitty pet", "happiness"},
		{"🦊", "Fox", "clever animal", "playfulness"},
		{"🐼", "Panda", "animal cute", "happiness"},
		{"🐸", "Frog", "animal cute", "playfulness"},
		{"🦄", "Unicorn", "magic rare", "excitement"},
		{"🐝", "Bee", "busy honey", "acknowledgement"},
		{"🦋", "Butterfly", "transform nature", "appreciation"},
		{"✈️", "Airplane", "travel flight", "excitement"},
		{"🚗", "Car", "drive travel", "acknowledgement"},
		{"🏠", "Home", "house home", "acknowledgement"},
		{"🏖️", "Beach", "holiday vacation", "excitement"},
		{"🗺️", "Map", "travel plan", "acknowledgement"},
		{"🎵", "Music", "song tune", "appreciation"},
		{"🎮", "Game", "play fun", "playfulness"},
		{"📷", "Camera", "photo picture", "appreciation"},
		{"🎨", "Art", "design creative", "appreciation"},
	}},
}

var (
	reactionEmojiSemantics = map[string]string{}
	reactionEmojiLabels    = map[string]string{}
	quickReactionEmojis    = []string{"👍", "❤️", "😂", "🎉", "👀", "✅"}
)

func init() {
	for _, category := range reactionEmojiCategories {
		for _, item := range category.Emojis {
			if _, exists := reactionEmojiSemantics[item.Emoji]; !exists {
				reactionEmojiSemantics[item.Emoji] = item.Semantic
				reactionEmojiLabels[item.Emoji] = item.Label
			}
		}
	}
}

func reactionCatalogPayload() map[string]any {
	categories := make([]map[string]any, 0, len(reactionEmojiCategories))
	for _, category := range reactionEmojiCategories {
		categories = append(categories, map[string]any{"name": category.Name, "emojis": category.Emojis})
	}
	return map[string]any{"categories": categories, "quick": quickReactionEmojis}
}
