package commands

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/xseman/superfaktura-cli/internal/output"
)

// The API takes an attachment as a base64 string inside the payload rather
// than as a multipart upload, and rejects anything outside a fixed list of
// types or over 4 MB. Both limits are checked here: a rejected upload costs a
// request out of a daily allowance of 1000, and finding out locally is free.
const maxAttachmentBytes = 4 << 20

// allowedAttachmentTypes is the list from expenses.md.
var allowedAttachmentTypes = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".tif": true, ".tiff": true,
	".gif": true, ".pdf": true, ".tmp": true, ".xls": true, ".xlsx": true,
	".ods": true, ".doc": true, ".docx": true, ".xml": true, ".csv": true,
	".msg": true, ".heic": true, ".isdoc": true,
}

// readAttachment loads a file and encodes it the way the API expects.
func readAttachment(path string) (string, error) {
	extension := strings.ToLower(filepath.Ext(path))
	if !allowedAttachmentTypes[extension] {
		return "", &output.Error{
			Code:    output.CodeUsage,
			Message: fmt.Sprintf("the API does not accept %q attachments", extension),
			Hint:    "Allowed: " + strings.Join(attachmentTypeList(), ", "),
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}
	if info.IsDir() {
		return "", &output.Error{
			Code:    output.CodeUsage,
			Message: fmt.Sprintf("%s is a directory", path),
		}
	}
	if info.Size() > maxAttachmentBytes {
		return "", &output.Error{
			Code: output.CodeUsage,
			Message: fmt.Sprintf("%s is %.1f MB; the API caps attachments at 4 MB",
				path, float64(info.Size())/(1<<20)),
		}
	}

	body, err := os.ReadFile(path) //nolint:gosec // G304: the path is the user's own argument
	if err != nil {
		return "", &output.Error{Code: output.CodeUsage, Message: err.Error()}
	}
	return base64.StdEncoding.EncodeToString(body), nil
}

func attachmentTypeList() []string {
	types := make([]string, 0, len(allowedAttachmentTypes))
	for extension := range allowedAttachmentTypes {
		types = append(types, strings.TrimPrefix(extension, "."))
	}
	slices.Sort(types)
	return types
}
