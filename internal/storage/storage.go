// Package storage defines the backend-neutral data access contract. No
// database driver types cross this boundary.
package storage

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Document is a stored document in API shape: "id" (string), "_meta"
// (map with time.Time values) and the user fields. Numbers are int64 or
// float64, nested objects are Documents or map[string]any, arrays []any.
type Document = map[string]any

// Query selects a page of documents.
type Query struct {
	// Filter is the client's where: a tree of conditions combined with
	// And and Or. nil matches everything.
	Filter Filter
	Sort   []SortField // applied in order; ends with "id" as a tiebreaker
	Limit  int
	Skip   int
	Count  bool // also compute the total number of matching documents
	// Access restricts the query to documents the caller may read
	// (from access rules); nil means no restriction.
	Access Filter
	// After restricts the page to the documents positioned after a cursor in
	// Sort (query.ParseCursor builds it). It narrows the page, not the Total.
	After Filter
}

// Page is one page of a list query.
type Page struct {
	Items   []Document
	HasMore bool
	Total   *int64 // set only when Query.Count is true
}

// Operator is a filter operator. Operators are backend-neutral; each
// storage implementation translates them.
type Operator string

const (
	OpEq          Operator = "$eq"
	OpNe          Operator = "$ne"
	OpGt          Operator = "$gt"
	OpGte         Operator = "$gte"
	OpLt          Operator = "$lt"
	OpLte         Operator = "$lte"
	OpIn          Operator = "$in"      // Value: []any
	OpNin         Operator = "$nin"     // Value: []any
	OpBetween     Operator = "$between" // Value: []any{min, max}, inclusive
	OpIsNull      Operator = "$isNull"  // Value: bool; true matches null or missing
	OpLike        Operator = "$like"    // Value: string; SQL-style % and _ wildcards
	OpILike       Operator = "$ilike"
	OpStartsWith  Operator = "$startsWith"
	OpIStartsWith Operator = "$istartsWith"
	OpEndsWith    Operator = "$endsWith"
	OpIEndsWith   Operator = "$iendsWith"
	OpContains    Operator = "$contains"
	OpIContains   Operator = "$icontains"
)

// Condition is one filter clause. Field is a dot path in API terms ("id",
// "_meta.created_at", "address.city"). Values are already type-checked:
// numbers are int64 or float64, _meta dates are time.Time.
type Condition struct {
	Field string
	Op    Operator
	Value any
}

// Filter is a boolean combination of conditions: a Condition, And, Or,
// Not or Const.
type Filter interface{ isFilter() }

func (Condition) isFilter() {}

// And matches documents matching every filter; And{} matches everything.
type And []Filter

// Or matches documents matching any filter; Or{} matches nothing.
type Or []Filter

// Not matches documents not matching Filter.
type Not struct{ Filter Filter }

// Const matches every document (true) or none (false).
type Const bool

func (And) isFilter()   {}
func (Or) isFilter()    {}
func (Not) isFilter()   {}
func (Const) isFilter() {}

// Simplify folds constants, so the result is a Const or contains none.
func Simplify(f Filter) Filter {
	switch f := f.(type) {
	case And:
		var out And
		for _, x := range f {
			switch x := Simplify(x).(type) {
			case Const:
				if !x {
					return Const(false)
				}
			default:
				out = append(out, x)
			}
		}
		switch len(out) {
		case 0:
			return Const(true)
		case 1:
			return out[0]
		}
		return out
	case Or:
		var out Or
		for _, x := range f {
			switch x := Simplify(x).(type) {
			case Const:
				if x {
					return Const(true)
				}
			default:
				out = append(out, x)
			}
		}
		switch len(out) {
		case 0:
			return Const(false)
		case 1:
			return out[0]
		}
		return out
	case Not:
		if c, ok := Simplify(f.Filter).(Const); ok {
			return !c
		}
		return Not{Simplify(f.Filter)}
	}
	return f
}

// SortField orders results by one field.
type SortField struct {
	Field string
	Desc  bool
}

// Repository gives access to the documents of one collection.
type Repository interface {
	// Create stores doc, which must already carry "id" and "_meta".
	Create(ctx context.Context, doc Document) error
	// Get returns the document with the given id, or ErrNotFound.
	Get(ctx context.Context, id string) (Document, error)
	// List returns a page of documents.
	List(ctx context.Context, q Query) (Page, error)
	// Replace overwrites the document with doc["id"] only if its stored
	// _meta.version equals ifVersion (0 means "stored without a version").
	// It returns ErrNotFound or ErrVersionMismatch otherwise.
	Replace(ctx context.Context, doc Document, ifVersion int64) error
	// Delete removes the document with the given id. When ifVersion is not
	// nil the stored version must equal it. It returns ErrNotFound or
	// ErrVersionMismatch otherwise.
	Delete(ctx context.Context, id string, ifVersion *int64) error
}

var (
	// ErrNotFound means no document has the requested id.
	ErrNotFound = errors.New("document not found")
	// ErrVersionMismatch means a conditional write found a different version.
	ErrVersionMismatch = errors.New("document version mismatch")
	// ErrUnavailable means the storage backend could not be reached in time.
	ErrUnavailable = errors.New("storage unavailable")
	// ErrConflict means a write violated a unique index. The concrete error
	// is a *ConflictError.
	ErrConflict = errors.New("unique constraint violated")
)

// ConflictError reports a unique index violation.
type ConflictError struct {
	Fields []string // fields of the violated index, in API terms; may be empty
}

func (e *ConflictError) Error() string {
	if len(e.Fields) == 0 {
		return ErrConflict.Error()
	}
	return ErrConflict.Error() + " on " + strings.Join(e.Fields, ", ")
}

// Is makes errors.Is(err, ErrConflict) match.
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

// Fetch returns the document with id if the access filter (nil: none)
// lets the caller read it, else ErrNotFound. Single reads use it, so a
// read rule applies to them exactly as it does to lists.
func Fetch(ctx context.Context, repo Repository, id string, filter Filter) (Document, error) {
	if filter == nil {
		return repo.Get(ctx, id)
	}
	page, err := repo.List(ctx, Query{
		Filter: Condition{Field: "id", Op: OpEq, Value: id},
		Access: filter,
		Limit:  1,
	})
	if err != nil {
		return nil, err
	}
	if len(page.Items) == 0 {
		return nil, ErrNotFound
	}
	return page.Items[0], nil
}

// Eraser is what erasing a user needs of a collection's storage. Every method
// works in batches and acts only on documents that still match, so a batch
// that was interrupted is simply repeated: call each until it returns 0.
// Updates are atomic per document (never read-then-write) and are system
// writes: they bump _meta.version and _meta.updated_at, and record
// _meta.updated_by as ErasedBy.
type Eraser interface {
	// CountOwned counts the documents whose _meta.owner is the user.
	CountOwned(ctx context.Context, owner string) (int64, error)
	// CountReferences counts the documents where field (an array of strings
	// when array is true, else a string) holds value.
	CountReferences(ctx context.Context, field, value string, array bool) (int64, error)
	// DeleteOwned deletes up to limit documents the user owns.
	DeleteOwned(ctx context.Context, owner string, limit int) (int64, error)
	// AnonymizeOwned removes the fields in remove, sets those in replace and
	// clears _meta.owner on up to limit documents the user owns.
	AnonymizeOwned(ctx context.Context, owner string, remove []string, replace map[string]any, limit int, now time.Time) (int64, error)
	// PullReference removes value from the array field of up to limit
	// documents that contain it.
	PullReference(ctx context.Context, field, value string, limit int, now time.Time) (int64, error)
	// ClearReference unsets the string field of up to limit documents where
	// it equals value.
	ClearReference(ctx context.Context, field, value string, limit int, now time.Time) (int64, error)
}

// Scanner reads every stored document of a collection, in id order, for the
// schema check: soft-deleted ones included, with no access rules and no
// writes. A repository that doesn't implement it can't be checked.
type Scanner interface {
	// ScanAfter returns up to limit documents whose id sorts after afterID
	// ("" starts from the first), ordered by id.
	ScanAfter(ctx context.Context, afterID string, limit int) ([]Document, error)
	// EstimatedCount is the collection's approximate size, cheap to ask.
	EstimatedCount(ctx context.Context) (int64, error)
}

// ErasedBy is what an erase records in _meta.updated_by.
const ErasedBy = "backd:erase"
