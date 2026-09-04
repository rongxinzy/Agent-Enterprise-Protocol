package httpapi

// page trims a fetch of limit+1 records and returns the stable ID cursor for
// the next page. A nil cursor is encoded as JSON null.
func page[T any](items []T, pageSize int32, id func(T) string) ([]T, *string) {
	if int32(len(items)) <= pageSize {
		return items, nil
	}
	items = items[:pageSize]
	next := id(items[len(items)-1])
	return items, &next
}
