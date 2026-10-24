---
title: "25. Private notes"
description: "Encrypt a field before it reaches MongoDB: create and save a key, mark the field, let only the author and the team's admins read it, look at the ciphertext, rotate the key and export the notes with it."
icon: "lock"
weight: 450
toc: true
---

A pin (chapter 20) has a `note` the whole team reads. Sometimes the person who pins something wants to add a note for themselves and their team's admins: a price they negotiated, a reason they don't want to repeat. This chapter adds **private notes** to pins and does what a database dump or a backup should never allow: it makes sure nobody reading them, however they got hold of the data, can read those notes without the key.

## The key

Encrypted fields use a **key you hold a copy of**. Create it, with the stack from the previous chapters running:

```sh
docker compose exec backd /backd secret create --direct --realm shelf --type key --generate --name SHELF_NOTES
```

```
3q2-7wXk…(43 characters)
Key SHELF_NOTES created, fingerprint SHELF_NOTES:9f2c41d0.
This is the only time the key is shown. Store it somewhere safe outside backd: if it is lost, every value encrypted with it is lost.
```

The key is printed once. Copy it to your password manager now, together with the fingerprint, which lets you check later that a saved copy is the right one. `--direct` works on MongoDB and `config/` straight from the command, which is why it needs no sign-in: it is also how you create a key `backd` refuses to start without.

{{< hint warning >}}
Nothing shows this key again: not the API, the command line or the admin interface. If you lose it **and** `backd` loses its own sealed copy, the notes are gone for good. Save it before you use it.
{{< /hint >}}

## The field

In `config/shelf/main/pins/schema.json` the pin gains a field like any other:

```json
"private_notes": { "type": "string", "maxLength": 2000 }
```

and in `collection.yaml`, three lines say that it is encrypted, with that key, and who sees it:

{{< example-file path="shelf/main/pins/collection.yaml" lines="18-23" >}}

`reveal` is an access rule that runs after the read rule has let someone see the pin: the **author**, and the team's **admins** and **owners**, read the notes; everybody else who can read the pin gets `null` in their place and the field's name in `_meta.redacted`. The `pin` function passes the notes along, and knows nothing about keys:

{{< example-file path="shelf/main/_functions/pin/index.ts" lines="27-30" >}}

Add `private_notes` to the function's `input.schema.json` too, regenerate the types (`backd types`, chapter 19) and restart `backd`. If the key were missing, `backd` would refuse to start and say which field names it and what to run. In `app.js` the pin form gets a "Private notes" box, and the list shows the notes with a lock, or says that they are hidden.

## You should see

Pin something with a private note as one member, then look at it as another:

- **The author and an admin** read the pin with `private_notes`; a **plain member** gets the pin with `private_notes: null` and `"_meta": {"redacted": ["private_notes"]}`, so the app can tell hidden from empty. Their `ETag` carries a suffix (`"1.r3f9a1c2b"`), so a member who is promoted never gets a stale `304`.
- A search by the note, `where={"private_notes": "…"}`, answers `400 field_encrypted`: an encrypted field can't be queried unless you ask for `search: equality` (see [Field keys](../../build/configuration/field-keys/#search-an-encrypted-field)).
- **MongoDB holds ciphertext.** Look at the document itself:

  ```sh
  docker compose exec mongo mongosh --quiet --eval 'EJSON.stringify(db.getSiblingDB("shelf__main").pins.findOne())'
  ```

  The title is there in clear; `private_notes` is `{"$binary": {"base64": "AaCh…", "subType": "06"}}`, and a backup of the database holds the same.

## Rotate the key

A key you have copied around, or an employee who left, is a reason to replace it. Replacing it is **instant**: the key only protects the keys that encrypt the notes, so nothing in the database is rewritten.

```sh
docker compose exec backd /backd secret rotate --direct --realm shelf --name SHELF_NOTES --generate
```

You get the new key once, with both fingerprints, and every pin still reads exactly as before (its `_meta.version` didn't move). Keep your **old** copy for backups made before today: a dump needs the key that was current when it was taken. See [Field keys](../../build/configuration/field-keys/#rotate-a-key) for when a rotation is not enough.

## Take the notes out of the stack

An export is where "controlled by the developer" becomes concrete: the server **never decrypts for an export**, so what you get is ciphertext unless you supply the key, on your machine:

```sh
docker compose exec backd /backd export --direct --realm shelf --db main --collection pins --no-files --out /tmp/pins
docker compose exec backd /backd export --direct --realm shelf --db main --collection pins --no-files --out /tmp/pins-open \
  --field 'private_notes=key:3q2-7wXk…'
docker compose cp backd:/tmp/pins-open/main/pins.jsonl .
```

The first package has the titles and the notes as ciphertext; the second has the notes in plain text, because you gave their key, and nothing else is opened. A wrong key is refused by its fingerprint. Read [Encrypted fields](../../operate/administration/export-import/encrypted-fields/) for importing such a package into another realm.

## What this does not protect

Encrypted fields protect against someone with the **database** but not the keys: a dump, a backup, a stolen disk, an operator with MongoDB access. They do not protect against a compromised `backd` process (it holds the keys while it runs), against someone who holds the secrets master key **and** the database, or against a function that prints a note to its logs. [Security](../../operate/administration/security/) lists it all.

The tutorial's [tour](https://github.com/fernandezvara/backd/blob/main/clients/js/examples/shelf/tour.js) checks everything on this page against a running stack.

Next: [chapter 26](../26-your-data/) — your data.
