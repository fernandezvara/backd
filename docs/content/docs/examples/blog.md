---
title: "Blog"
description: "A blog with public posts and private drafts: authors, ownership and filters with access rules."
icon: "article"
weight: 710
toc: true
---

A small blog. Anyone can read published posts. Signed-in users write posts, keep drafts, and edit, publish, unpublish and delete their own. Each post shows when and by whom it was written, and its category. Clicking an author or a category lists only those posts, and both filters combine.

It shows the everyday use of backd: public and private documents in one collection, ownership, and posts signed with the writer's own identity, all decided on the server by [access rules](../../auth/rules/). Unlike the [expenses example](../expenses/), it doesn't share documents between users, so rules cover everything it needs: it has no known holes, only the [limits](#limits) below. One page, above the post list, does need a [server-side function](#a-server-side-function-stats): a count of every published post, and of the caller's own, neither computable from what a reader may list.

The configuration is the `blog` realm in `examples/config/blog`. The application is in `clients/js/examples/blog`.

{{< live-example >}}

## Try it

```sh
make example
```

Open <https://localhost:8443/example/blog/> (see [HTTPS with a local CA](../../getting-started/#https-with-a-local-ca) to avoid the certificate warning). Sign up, write a post as a draft, then publish it. Log out: the published post is still there, and the draft isn't. Sign up as a second user in a private window: you can read the first user's published posts, but not edit them.

The app is plain JavaScript with [Alpine.js](https://alpinejs.dev/) and the [JavaScript client](../../clients/js/), with no build step:

| File | Contents |
|---|---|
| `app.js` | Sign-up and login, posts, drafts, editing, filters, the [stats function](#a-server-side-function-stats) (about 210 lines) |
| `index.html`, `style.css` | The page |

- The page, the API and these docs are served by nginx on one origin, `https://localhost:8443`, so the app calls the API with `url: window.location.origin` and needs no [CORS settings](../../configuration/realm/#cors).
- The page imports `@backd/client` through an [import map](https://developer.mozilla.org/docs/Web/HTML/Element/script/type/importmap), as an app built with a bundler would.
- The session survives reloads because the app keeps it in `localStorage`.

## Data model

One collection, `posts`, in the database `main`:

| Field | Type | Notes |
|---|---|---|
| `title` | string | Required, never empty |
| `body` | string | |
| `published` | boolean | Readers see only published posts; a post without it is a draft |
| `category` | string | Optional, 1 to 40 characters, such as `news` |
| `author` | object | `name` and `email`; the rules require `author.email` to be the writer's own |

- **Ownership comes from backd.** `_meta.owner` is set to the signed-in writer when a post is created and never changes, so the rules use it to decide who may edit and delete. `author` is the post's visible byline.
- **Filters are list queries** such as `posts.list({ where: { 'author.email': email, category: 'news' } })`. The server combines them with the read rule, so a filter can only narrow what the caller may see. `indexes.json` indexes `published`, `category` and `_meta.owner`, each with the creation date, so the lists stay fast.
- **Edits can't overwrite each other.** Saving sends the changes as a JSON merge patch with `ifMatch: post._meta.version`. If the post changed meanwhile (in another tab, on another device), backd answers `412` and the app reloads the post and asks to try again. An empty category is sent as `null`, which removes the field.
- The realm has `signup: open`, so anyone can create an account. In public, rate-limit sign-up at the proxy, as the [production reference](../../operations/production/) does.

## What the rules enforce

`posts/rules.yaml`:

| Rule | Enforces |
|---|---|
| `read: document.published == true \|\| (user != nil && document._meta.owner == user.id)` | Anyone reads published posts; drafts are visible only to their author |
| `create: user != nil && data.author.email == user.email` | Only signed-in users write, and only with their own email as the author |
| `update: user != nil && document._meta.owner == user.id && !('author' in changed())` | Only the writer edits a post, and never changes its author, not even the name |
| `delete: user != nil && document._meta.owner == user.id` | Only the writer deletes it |

The read rule is applied inside the database query: lists, pages and counts only ever include what the caller may read, and a draft answers `404` to anyone else, as if it didn't exist.

### Rules to avoid

Tempting shortcuts, and what they would allow:

| Instead of | A naive rule | Would let… |
|---|---|---|
| the read rule above | `read: "true"` | anyone read every draft |
| `data.author.email == user.email` | `create: user != nil` | any signed-in user publish posts signed with someone else's email |
| the owner check on update | `update: user != nil` | any signed-in user edit anyone's posts |
| `!('author' in changed())` | no check | authors re-attribute their posts to someone else |
| checking `published` in the app | a filter in `app.js` only | anyone read drafts by calling the API directly |

A test (`internal/httpapi/example_blog_test.go`) runs every one of these attacks against the real configuration and checks that the rules stop them.

## A server-side function: stats

The page shows two counts above the post list: how many posts are published, and how many are yours. Neither is something a reader can compute from what the rules let them list:

- **The published count needs every published post**, not just a page of them (`posts.list({ limit: 1, count: true })` gives an exact total without fetching every document, but the caller still can't compute it *themselves* from a list they can only see part of at a time without an extra round trip — the point here is that it's one call to a function that already knows the answer, not a client-side reduction over paginated results).
- **"Yours" needs posts the caller couldn't list as themselves in the first place**, for a signed-out visitor: nothing (drafts are unreadable to anyone but their author, so a signed-out caller genuinely has no way to know).

`examples/config/blog/main/_functions/stats/index.ts`:

```ts
export default async function handler(ctx: Context) {
  const posts = ctx.db("main").collection("posts");
  const published = await posts.list({ where: { published: true }, limit: 1, count: true });
  const mine = ctx.user
    ? await posts.list({ where: { "_meta.owner": ctx.user.id }, limit: 1, count: true })
    : { total: 0 };
  return { published: published.total ?? 0, mine: mine.total ?? 0 };
}
```

- `mode: sync` (the default) and `invoke: "true"`: anyone may call it, published or not — it only ever returns counts, never a post's content, so publishing it anonymously is fine here. See [Invoke rules](../../functions/reference/#functionyaml) for what else `invoke` can express.
- `ctx.db` is the [JS client](../../clients/js/), acting as the caller: rules still apply, exactly as if the caller had made these calls themselves — the function doesn't need `admin: true`, since everything it reads is already something the rules would let the right caller see; it just does the counting in one call.
- The app calls it with `backd.db('main').fn('stats')` and shows the result at the top of the page, refreshed after every write that could change a count. See the [JS client's functions guide](../../clients/js/#functions).
- This exact function is what `backd template database --realm <realm> --database <database> --sample` and `backd template realm --realm <realm> --sample` scaffold, alongside the sample `posts` collection — try it: `backd template realm --realm demo --sample` and look in `demo/main/_functions/stats/`.

## Erasing an author

When an administrator [erases a user](../../auth/erasure/), their posts stay (a blog's archive keeps its articles) but stop pointing at them. `posts/collection.yaml` says how:

{{< example-file path="blog/main/posts/collection.yaml" >}}

The byline's email is removed, its name replaced, and `_meta.owner` cleared, so nobody can edit the posts any more. `backd user owned --realm blog --email …` previews it. `backd template database --sample` writes the same file.

## Limits

What the rules above don't cover, by design or until planned features exist. The same test checks each one:

| Limit | Why | What would change it |
|---|---|---|
| **The author's display name is free text.** Bob can publish as "Ada Lovelace" with his own email. | Only `author.email` is checked against the writer; a name can't be. | Showing the email, or a profile collection the app reads names from |
| **Unverified emails can post.** | This realm sends no email (it is also the [production reference](../../operations/production/), which has no mail provider), so nobody could verify an address and the rule doesn't require `user.email_verified`. | A realm with [email](../../functions/email/) and `&& user.email_verified` in the create rule, as the [expenses example](../expenses/) does |
| **A post without `published` is a draft.** | In a read rule, a missing field never equals `true` (see [missing and null fields](../../auth/rules/#missing-and-null-fields)). | Nothing: this is the intended behavior. The app always sends `published` |
| **Published posts are public, so they can be scraped.** | That's what `read` allows for anonymous callers. | Rate limits or a CDN in front, as in the [production reference](../../operations/production/#rate-limits) |

## Best practices

What this example follows, and what applies to any app on backd:

- Decide visibility in the read rule, never only in the app: the API is reachable without the app.
- Use `_meta.owner` for ownership, set by backd, rather than a field the client writes.
- When a document names its writer, check that name against `user` in the create rule, and freeze it with `changed()` in the update rule.
- Send edits with `ifMatch`, so concurrent changes are reported instead of lost.
- Test the attacks, not just the happy path.
