package plugin

import "os"

// OpenRoot prevents reparse-point escapes; Lstat plus the opened handle's identity
// rejects final symlinks and substitutions. Windows regular disk files do not block like Unix FIFOs.
func publishReadFlags() int { return os.O_RDONLY }
