package errs

import "errors"

// As — тонкая обёртка, чтобы пакет не тянул errors во все файлы.
func As(err error, target any) bool { return errors.As(err, target) }
