---
title: "Rules for files"
description: "How access rules decide who uploads, removes and downloads files, and how to reserve a file field for functions."
icon: "policy"
weight: 578
toc: true
---

Files have no rules of their own: they follow the [rules of their document](../../auth/rules/).

| Operation | Rule |
|---|---|
| Upload, remove one, clear a field | `update` |
| Download, `?link=json`, `?file_links=true` | `read` |

File fields are known to the rules like any other field of the collection, so a rule can read them from `document` and `data`. For a file operation:

- `document` is the document as stored; `data` is the document **as it would be after the change**; `changed()` lists the changed fields, which includes the file field.
- The rule runs **before any byte is stored**: a refused upload costs nothing and leaves nothing behind.
- API keys and `ctx.admin.db` in functions skip rules.

## Reserving a field for functions

A field only a function may set, such as a generated thumbnail, is a file field the rule refuses to clients:

```yaml
# collection.yaml
rules:
  update: >
    user != nil && document._meta.owner == user.id
    && !('thumbnail' in changed())
```

An owner who uploads to or removes `thumbnail` gets `403`; a function calling `ctx.admin.db` sets it and nothing in the rules stands in the way. A rule on `create` can refuse the field as well, see [Creating with files](#creating-with-files).

## Creating with files

A [pending upload](../transfers/#creating-a-document-with-its-files) named in a create, `PUT` or `PATCH` is checked by the `create` or `update` rule **on the final document**, with the file's details already in the field: the same rule for a signed-in caller and an anonymous one. On a create there is no `changed()`, so a rule that reserves a field refuses it with `data.thumbnail == nil`:

```yaml
create: user != nil && data.thumbnail == nil
```

A rule that refuses leaves the upload unused. Starting a pending upload is itself only a question of the `create` rule existing (and, for an anonymous caller, admitting a document with just the file); what the document may hold is decided when it is written.

## Limits by what is already there

Because `data` is the document after the change, a rule can limit by content, such as *at most 3 receipts on a draft*: `update: size(data.receipts) <= 3 || data.status != 'draft'`. `max_files` is the hard limit and answers `409` before anything is read; a rule is for limits that depend on the document.
