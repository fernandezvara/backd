package httpapi

// mergePatch applies an RFC 7396 JSON Merge Patch object to target, which
// is modified in place and returned. A null value removes the member.
func mergePatch(target, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(target, k)
		case map[string]any:
			tv, _ := target[k].(map[string]any)
			target[k] = mergePatch(tv, pv)
		default:
			target[k] = v
		}
	}
	return target
}
