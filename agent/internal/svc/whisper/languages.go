package whisper

import (
	"regexp"
	"strings"
)

// whisper-server's verbose_json names the detected language in English ("english"); the agent's answer
// carries the ISO code ("en"). The table is whisper.cpp's language list.
var languageNames = map[string]string{
	"english": "en", "chinese": "zh", "german": "de", "spanish": "es", "russian": "ru", "korean": "ko",
	"french": "fr", "japanese": "ja", "portuguese": "pt", "turkish": "tr", "polish": "pl", "catalan": "ca",
	"dutch": "nl", "arabic": "ar", "swedish": "sv", "italian": "it", "indonesian": "id", "hindi": "hi",
	"finnish": "fi", "vietnamese": "vi", "hebrew": "he", "ukrainian": "uk", "greek": "el", "malay": "ms",
	"czech": "cs", "romanian": "ro", "danish": "da", "hungarian": "hu", "tamil": "ta", "norwegian": "no",
	"thai": "th", "urdu": "ur", "croatian": "hr", "bulgarian": "bg", "lithuanian": "lt", "latin": "la",
	"maori": "mi", "malayalam": "ml", "welsh": "cy", "slovak": "sk", "telugu": "te", "persian": "fa",
	"latvian": "lv", "bengali": "bn", "serbian": "sr", "azerbaijani": "az", "slovenian": "sl", "kannada": "kn",
	"estonian": "et", "macedonian": "mk", "breton": "br", "basque": "eu", "icelandic": "is", "armenian": "hy",
	"nepali": "ne", "mongolian": "mn", "bosnian": "bs", "kazakh": "kk", "albanian": "sq", "swahili": "sw",
	"galician": "gl", "marathi": "mr", "punjabi": "pa", "sinhala": "si", "khmer": "km", "shona": "sn",
	"yoruba": "yo", "somali": "so", "afrikaans": "af", "occitan": "oc", "georgian": "ka", "belarusian": "be",
	"tajik": "tg", "sindhi": "sd", "gujarati": "gu", "amharic": "am", "yiddish": "yi", "lao": "lo",
	"uzbek": "uz", "faroese": "fo", "haitian creole": "ht", "pashto": "ps", "turkmen": "tk", "nynorsk": "nn",
	"maltese": "mt", "sanskrit": "sa", "luxembourgish": "lb", "myanmar": "my", "tibetan": "bo", "tagalog": "tl",
	"malagasy": "mg", "assamese": "as", "tatar": "tt", "hawaiian": "haw", "lingala": "ln", "hausa": "ha",
	"bashkir": "ba", "javanese": "jw", "sundanese": "su", "cantonese": "yue",
}

// LanguageCode maps a language as whisper-server names it to its ISO code. A value that already looks
// like a code (two or three letters) is returned in lower case; a name the table does not know is
// returned as it came, lower-cased.
func LanguageCode(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if code, ok := languageNames[n]; ok {
		return code
	}
	return n
}

// languageRe is what the language field of a request may hold (implementation note 5 of the issue).
var languageRe = regexp.MustCompile(`^(?:[a-z]{2,3}|auto)$`)

// ValidLanguage reports whether s is "auto" or a two or three letter lower-case code.
func ValidLanguage(s string) bool { return languageRe.MatchString(s) }
