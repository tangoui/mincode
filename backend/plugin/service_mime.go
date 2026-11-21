package plugin

import (
	"path/filepath"
	"strings"
)

// serviceMimeTypes is the built-in extension → MIME table used to expand MIME
// accepts of contributes.services file input (image/*, application/pdf) and to
// label exchanged files. MinCode matches by extension only and never sniffs
// content, so the table is deliberately fixed and identical on every OS. Keep
// it identical to SERVICE_MIME_TYPES in vue-app/src/domain/plugins/services.ts
// (vue-app/scripts/services-contributions.test.mjs compares them).
var serviceMimeTypes = map[string]string{
	// Images
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".gif":  "image/gif",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".tif":  "image/tiff",
	".tiff": "image/tiff",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".heic": "image/heic",
	".heif": "image/heif",
	".avif": "image/avif",
	".jxl":  "image/jxl",
	".psd":  "image/vnd.adobe.photoshop",
	".djvu": "image/vnd.djvu",
	// Audio
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".opus": "audio/opus",
	".flac": "audio/flac",
	".aac":  "audio/aac",
	".m4a":  "audio/mp4",
	".weba": "audio/webm",
	".mid":  "audio/midi",
	".midi": "audio/midi",
	".aif":  "audio/aiff",
	".aiff": "audio/aiff",
	".amr":  "audio/amr",
	// Video
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mov":  "video/quicktime",
	".webm": "video/webm",
	".mkv":  "video/x-matroska",
	".avi":  "video/x-msvideo",
	".wmv":  "video/x-ms-wmv",
	".flv":  "video/x-flv",
	".mpeg": "video/mpeg",
	".mpg":  "video/mpeg",
	".ogv":  "video/ogg",
	".3gp":  "video/3gpp",
	// Fonts
	".ttf":   "font/ttf",
	".otf":   "font/otf",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttc":   "font/collection",
	// Documents
	".pdf":  "application/pdf",
	".ofd":  "application/ofd",
	".rtf":  "application/rtf",
	".doc":  "application/msword",
	".dot":  "application/msword",
	".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".dotx": "application/vnd.openxmlformats-officedocument.wordprocessingml.template",
	".docm": "application/vnd.ms-word.document.macroenabled.12",
	".xls":  "application/vnd.ms-excel",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".xltx": "application/vnd.openxmlformats-officedocument.spreadsheetml.template",
	".xlsm": "application/vnd.ms-excel.sheet.macroenabled.12",
	".xlsb": "application/vnd.ms-excel.sheet.binary.macroenabled.12",
	".ppt":  "application/vnd.ms-powerpoint",
	".pptx": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".ppsx": "application/vnd.openxmlformats-officedocument.presentationml.slideshow",
	".potx": "application/vnd.openxmlformats-officedocument.presentationml.template",
	".pptm": "application/vnd.ms-powerpoint.presentation.macroenabled.12",
	".vsdx": "application/vnd.ms-visio.drawing",
	".odt":  "application/vnd.oasis.opendocument.text",
	".ods":  "application/vnd.oasis.opendocument.spreadsheet",
	".odp":  "application/vnd.oasis.opendocument.presentation",
	".odg":  "application/vnd.oasis.opendocument.graphics",
	".odf":  "application/vnd.oasis.opendocument.formula",
	".ott":  "application/vnd.oasis.opendocument.text-template",
	// E-books
	".epub": "application/epub+zip",
	".mobi": "application/x-mobipocket-ebook",
	".azw":  "application/vnd.amazon.ebook",
	".azw3": "application/vnd.amazon.ebook",
	".fb2":  "application/x-fictionbook+xml",
	".cbz":  "application/vnd.comicbook+zip",
	// Archives
	".zip": "application/zip",
	".tar": "application/x-tar",
	".gz":  "application/gzip",
	".tgz": "application/gzip",
	".bz2": "application/x-bzip2",
	".xz":  "application/x-xz",
	".zst": "application/zstd",
	".7z":  "application/x-7z-compressed",
	".rar": "application/vnd.rar",
	".jar": "application/java-archive",
	".iso": "application/x-iso9660-image",
	// Text and structured data
	".txt":      "text/plain",
	".log":      "text/plain",
	".md":       "text/markdown",
	".markdown": "text/markdown",
	".csv":      "text/csv",
	".tsv":      "text/tab-separated-values",
	".html":     "text/html",
	".htm":      "text/html",
	".xhtml":    "application/xhtml+xml",
	".css":      "text/css",
	".json":     "application/json",
	".jsonl":    "application/jsonl",
	".ndjson":   "application/x-ndjson",
	".geojson":  "application/geo+json",
	".ipynb":    "application/x-ipynb+json",
	".xml":      "application/xml",
	".yaml":     "application/yaml",
	".yml":      "application/yaml",
	".toml":     "application/toml",
	".ics":      "text/calendar",
	".vcf":      "text/vcard",
	".srt":      "application/x-subrip",
	".vtt":      "text/vtt",
	".sql":      "application/sql",
	".sqlite":   "application/vnd.sqlite3",
	".parquet":  "application/vnd.apache.parquet",
	".wasm":     "application/wasm",
	// Source code
	".js":    "text/javascript",
	".mjs":   "text/javascript",
	".cjs":   "text/javascript",
	".py":    "text/x-python",
	".go":    "text/x-go",
	".java":  "text/x-java",
	".kt":    "text/x-kotlin",
	".c":     "text/x-c",
	".h":     "text/x-c",
	".cpp":   "text/x-c++",
	".cc":    "text/x-c++",
	".hpp":   "text/x-c++",
	".cs":    "text/x-csharp",
	".rs":    "text/x-rust",
	".rb":    "text/x-ruby",
	".php":   "text/x-php",
	".swift": "text/x-swift",
	".sh":    "application/x-sh",
}

// serviceMimeLookup returns the MIME type of fileName's last extension
// (case-insensitive) and whether the table knows it.
func serviceMimeLookup(fileName string) (string, bool) {
	mime, ok := serviceMimeTypes[strings.ToLower(filepath.Ext(fileName))]
	return mime, ok
}

// serviceMimeForName returns the MIME type of fileName, or
// application/octet-stream for an unknown extension.
func serviceMimeForName(fileName string) string {
	if mime, ok := serviceMimeLookup(fileName); ok {
		return mime
	}
	return "application/octet-stream"
}

// serviceAccepts reports whether fileName matches one of a service's accepts
// entries: a lower-case extension (".pdf", ".tar.gz") matches the end of the
// lower-cased name; a MIME type ("application/pdf") or wildcard ("image/*")
// matches through the built-in table, so a file whose extension the table does
// not know matches no MIME entry.
func serviceAccepts(accepts []string, fileName string) bool {
	name := strings.ToLower(fileName)
	mime, known := serviceMimeLookup(name)
	for _, accept := range accepts {
		switch {
		case strings.HasPrefix(accept, "."):
			if strings.HasSuffix(name, accept) {
				return true
			}
		case !known:
		case strings.HasSuffix(accept, "/*"):
			if strings.HasPrefix(mime, strings.TrimSuffix(accept, "*")) {
				return true
			}
		case accept == mime:
			return true
		}
	}
	return false
}
