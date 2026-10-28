// Seeds the example realms of a running local stack (make example, make dev-examples).
// Runs inside the executor container — the Deno image — and uses the repository's own
// JS client, so the sign-in chain (consents, a second factor, enrolling one) is done
// the same way the apps do it:
//
//   docker compose exec -T executor deno run --allow-all /seed/seed.mjs       — seeds
//   docker compose exec -T executor deno run --allow-all /seed/seed.mjs plan  — what to bootstrap
//
// (/seed is ./scripts, /client is ./clients/js and /seed-config is CONFIG_DIR — all
// mounted read-only; the API is at API_URL, http://backd:8080 inside the network.)
//
// Nothing here is a list: the realms are the directories of SEED_CONFIG and what to
// seed in each is read from its realm.yaml — the signup mode (open: sign up; invite
// or closed: the realm's administrator creates the account), the `roles.<role>.users`
// seed assignments (they are the demo accounts; a full administrator goes first so
// it can create the rest), `account.require_verified_email` (verified through the
// development outbox, in `email.function`'s database) and `storage:` (the local
// MinIO's keys are set as the realm's secrets). A realm whose realm.yaml can't be
// read is warned about and skipped; a realm with no seeded users and no storage is
// passed by. What a realm can't say — the example app's path, demo accounts that
// hold no role — a seed.json next to the realm.yaml adds:
//   { "app": "/example/x/", "accounts": [{ "email": "…", "note": "…" }] }
// Add or remove a realm and this picks it up; extend one and edit its own files.
//
// Per account: sign it in first (an existing account answers at once and no mail is
// sent), create it when the realm refuses — signup on open ones, `admin.users.create`
// through the realm's administrator on invite/closed ones — and sign it in answering
// every step: consents, TOTP enrollment, a second factor. `plan` prints the seeded
// full administrator of each realm signup can't create one on, as "realm<TAB>email"
// lines for `backd bootstrap` (a local command of the API host, not an API).
//
// What a sign-in shows only once (the TOTP QR, its secret, the recovery codes) is
// kept: the credentials document is printed on stdout and the caller writes it in
// local-seed/ (gitignored, served by nginx at /local-seed/, rendered by the welcome
// page at /). The previous file is read from PREV_CREDENTIALS: on a rerun over kept
// data its stored secret is what answers the second factor.
//
// Environment: API_URL (http://backd:8080), CLIENT_URL (file:///client/src/index.js),
// SEED_CONFIG (/seed-config), PREV_CREDENTIALS (/local-seed/credentials.json),
// EXAMPLE_PASSWORD or EXAMPLE_<REALM>_PASSWORD (dev-p4ssw0rd!).

// Loaded lazily — `plan` only reads the configuration and never needs it.
const { createClient } = Deno.args[0] === "plan"
  ? { createClient: null }
  : await import(Deno.env.get("CLIENT_URL") ?? "file:///client/src/index.js");

const api = Deno.env.get("API_URL") ?? "http://backd:8080";
const configDir = Deno.env.get("SEED_CONFIG") ?? "/seed-config";
const prevFile = Deno.env.get("PREV_CREDENTIALS") ?? "/local-seed/credentials.json";
const devPassword = "dev-p4ssw0rd!";
const password = (realm) =>
  Deno.env.get(`EXAMPLE_${realm.toUpperCase().replace(/-/g, "_")}_PASSWORD`) ??
  Deno.env.get("EXAMPLE_PASSWORD") ??
  devPassword;

const note = (msg) => console.error(`  ${msg}`);
const warn = (msg) => console.error(`  (${msg})`);

// RFC 6238 TOTP: base32 decode (RFC 4648), HMAC-SHA1, 30-second steps, 6 digits.
const totp = async (secret) => {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = 0, value = 0;
  const key = [];
  for (const ch of secret.replace(/=+$/, "").replace(/\s/g, "").toUpperCase()) {
    const i = alphabet.indexOf(ch);
    if (i < 0) continue;
    value = (value << 5) | i;
    bits += 5;
    if (bits >= 8) {
      key.push((value >>> (bits - 8)) & 0xff);
      bits -= 8;
    }
  }
  const counter = Math.floor(Date.now() / 30000);
  const msg = new Uint8Array(8);
  for (let i = 7, c = counter; i >= 0; i--, c = Math.floor(c / 256)) msg[i] = c & 0xff;
  const hmac = await crypto.subtle.importKey(
    "raw", new Uint8Array(key), { name: "HMAC", hash: "SHA-1" }, false, ["sign"],
  );
  const h = new Uint8Array(await crypto.subtle.sign("HMAC", hmac, msg));
  const off = h[19] & 0x0f;
  return String(
    (((h[off] & 0x7f) << 24) | (h[off + 1] << 16) | (h[off + 2] << 8) | h[off + 3]) %
      1e6,
  ).padStart(6, "0");
};

// The credentials the previous run recorded: its stored TOTP secret is a rerun's
// answer to `mfa_required`. A missing or empty file starts empty.
const prev = JSON.parse(
  (await Deno.readTextFile(prevFile).catch(() => "")).trim() || '{"accounts":[]}',
);
const stored = (realm, email) =>
  prev.accounts?.find((a) => a.realm === realm && a.email === email);

// Signs in, answering every step the realm can come back with, until the client
// holds the session. Resolves with the one-off material to keep —
// { mfa, recovery_codes } — or null when it could not get in.
const signIn = async (client, realm, email, pw) => {
  let res = await client.auth.signIn({ email, password: pw });
  const keep = {};
  for (let i = 0; i < 6; i++) {
    if (res?.token || res?.session_id) return keep;
    if (res?.consentRequired) {
      res = await client.auth.acceptConsents(
        res.challenge,
        Object.fromEntries(res.consents.map((c) => [c.name, c.version])),
      );
    } else if (res?.mfaRequired) {
      const before = stored(realm, email);
      if (!before?.mfa?.secret) {
        warn(`${email} has a second factor but ${prevFile} doesn't hold its secret:`);
        warn(`mfa-reset it, then seed again: make dev-backd ARGS='user mfa-reset --realm ${realm} --email ${email} --local --yes'`);
        return null;
      }
      res = await client.auth.verifyMfa(
        res.challenge,
        "totp",
        await totp(before.mfa.secret),
      );
      keep.mfa = before.mfa; // the credentials shown before are still this factor's
      keep.recovery_codes = before.recovery_codes ?? [];
    } else if (res?.enrollmentRequired) {
      const setup = await client.auth.mfa.setup({ challenge: res.challenge });
      const out = await client.auth.mfa.confirm({
        setupId: setup.setup_id,
        code: await totp(setup.secret),
        challenge: res.challenge,
      });
      keep.mfa = {
        secret: setup.secret,
        otpauth_uri: setup.otpauth_uri,
        qr: setup.qr,
      };
      keep.recovery_codes = out.recovery_codes ?? [];
      res = out.session ?? out.consent ?? {}; // a consent step may still follow
    } else {
      return null;
    }
  }
  return null;
};

// The realms whose email "delivery" is a development capture function (it stores
// each message in the `outbox` collection of its own database — the Mailbox app
// reads it) verify an address the way a person would: the mail's link carries a
// token, POSTed to verify-email. A worker delivers, so it may take a moment.
const verifyEmail = async (client, realm, db, email) => {
  const outbox = client.db(db).collection("outbox");
  for (let i = 0; i < 60; i++) {
    const page = await outbox.list({ orderBy: "-_meta.created_at", limit: 100 })
      .catch(() => null);
    const mail = page?.items?.find((m) =>
      m.kind === "verify-email" && m.to?.includes(email.toLowerCase())
    );
    if (mail?.link) {
      const token = new URL(mail.link).searchParams.get("token");
      const res = await fetch(`${api}/v1/${realm}/_auth/verify-email`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token }),
      });
      if (res.ok) return true;
    }
    await new Promise((r) => setTimeout(r, 150));
  }
  warn(`no verification email for ${email} in the outbox (is a worker running?)`);
  return false;
};

// Reads the parts of a realm.yaml the seed needs — a narrow extractor, not a YAML
// parser, fine for files of this repository: top-level scalars (auth, signup),
// two-level keys (account.require_verified_email, email.function,
// mfa.required_roles), storage's presence, and roles.<name>.
// {admin, description, users} (block or inline-map form). A line it can't place
// throws: the caller skips the realm with a warning.
const scalar = (v) => (v ?? "").trim().replace(/^["']|["']$/g, "");
const inlineList = (v) => {
  const m = /^\[(.*)\]$/.exec(v ?? "");
  return m ? m[1].split(",").map(scalar).filter(Boolean) : null;
};
const readRealmYaml = (text) => {
  const s = {
    auth: "enabled", signup: "closed", verifyEmail: false,
    emailDb: "notifications", hasStorage: false, hasPublicStorage: false, roles: {},
  };
  let section = "", role = null, list = null;
  for (const raw of text.split("\n")) {
    const line = raw.replace(/(^|\s)#.*$/, ""); // comments
    if (!line.trim()) continue;
    const indent = line.length - line.trimStart().length;
    const t = line.trim();
    if (t.startsWith("-")) {
      // An item of the block list being collected ("- x", "- email: x" or
      // "- {email: x, …}"); anything else in the entry is not the seed's.
      const m = /email\s*:\s*["']?([^,"'\s{}\]]+)|^-\s*["']?([^,"'\s{}\]]+)/.exec(t);
      if (list && m) list.push(scalar(m[1] ?? m[2]));
      continue;
    }
    list = null;
    const m = /^([\w-]+)\s*:\s*(.*)$/.exec(t);
    if (!m) throw new Error(`line not understood: "${t}"`);
    const [, key, val] = m;
    if (indent === 0) {
      section = key;
      role = null;
      if (key === "auth") s.auth = scalar(val) || "enabled";
      else if (key === "signup") s.signup = scalar(val) || "closed";
      else if (key === "storage") s.hasStorage = true;
      else if (key === "public_storage") s.hasPublicStorage = true;
      continue;
    }
    if (section === "account" && indent === 2 && key === "require_verified_email") {
      s.verifyEmail = scalar(val) === "true";
    } else if (section === "email" && indent === 2 && key === "function") {
      s.emailDb = scalar(val).split("/")[0] || s.emailDb;
    } else if (section === "roles") {
      if (indent === 2) { // a role's name
        role = key;
        const r = (s.roles[role] ??= { admin: null, adminAreas: null, users: [], description: "" });
        const im = /^\{(.*)\}$/.exec(val); // inline map: name: {k: v, …}
        if (im) {
          for (const part of im[1].split(/,(?![^\[]*\])/)) { // commas outside []
            const kv = /^([\w-]+)\s*:\s*(.*)$/.exec(part.trim());
            if (kv?.[1] === "description") r.description = scalar(kv[2]);
            else if (kv?.[1] === "admin") {
              r.adminAreas = inlineList(kv[2]);
              r.admin = scalar(kv[2]) === "true" ? "full" : r.adminAreas ? "areas" : scalar(kv[2]) || null;
            } else if (kv?.[1] === "users") {
              r.users.push(...(inlineList(kv[2]) ?? []));
            }
          }
        }
        continue;
      }
      if (!role) continue;
      const r = s.roles[role];
      if (key === "description") r.description = scalar(val);
      else if (key === "admin") {
        r.adminAreas = inlineList(val);
        r.admin = scalar(val) === "true" ? "full" : r.adminAreas ? "areas" : scalar(val) || null;
      } else if (key === "users") {
        const il = inlineList(val);
        if (il) r.users.push(...il);
        else if (!val) list = r.users;
      }
    }
  }
  return s;
};

// The realms to seed: a spec per directory of SEED_CONFIG that has a readable
// realm.yaml — one that can't be read is skipped with a warning, never fatal —
// with the optional seed.json merged on top (its "app" path and the demo
// accounts that hold no role). Realm order is the directory listing's.
const realms = {};
for (const e of Deno.readDirSync(configDir)) {
  if (!e.isDirectory || e.name.startsWith(".")) continue;
  const dir = `${configDir}/${e.name}`;
  let s;
  try {
    s = readRealmYaml(await Deno.readTextFile(`${dir}/realm.yaml`));
  } catch (err) {
    if (!(err instanceof Deno.errors.NotFound)) {
      warn(`${e.name}: realm.yaml skipped (${err?.message ?? err})`);
    }
    continue;
  }
  if (s.auth === "disabled") continue;
  let seed = {};
  try {
    seed = JSON.parse(await Deno.readTextFile(`${dir}/seed.json`));
  } catch (err) {
    if (!(err instanceof Deno.errors.NotFound)) {
      warn(`${e.name}: seed.json ignored (${err?.message ?? err})`);
    }
  }
  s.app = typeof seed.app === "string" ? seed.app : `/example/${e.name}/`;
  const seen = new Set();
  s.accounts = [];
  const push = (x) => {
    const email = scalar(x?.email ?? "").toLowerCase();
    if (!/^\S+@\S+$/.test(email) || seen.has(email)) return;
    seen.add(email);
    s.accounts.push({ ...x, email });
  };
  // The seeded users of the realm's own roles, a full administrator first so it
  // can create the rest on realms where signup isn't open.
  const ranked = [];
  for (const [name, r] of Object.entries(s.roles)) {
    for (const u of r.users) {
      ranked.push({ email: u, role: name, admin: r.admin, note: r.description || `role: ${name}` });
    }
  }
  ranked.sort((a, b) =>
    (b.admin === "full") - (a.admin === "full") || (b.admin != null) - (a.admin != null)
  );
  for (const u of ranked) push(u);
  if (Array.isArray(seed.accounts)) for (const x of seed.accounts) push(x);
  realms[e.name] = s;
}

// `plan` is the wrapper's input for `backd bootstrap`: the seeded full
// administrator of every realm signup can't create one on.
if (Deno.args[0] === "plan") {
  for (const name of Object.keys(realms).sort()) {
    const s = realms[name];
    if (s.signup === "open") continue;
    const admin = Object.values(s.roles).find((r) => r.admin === "full" && r.users.length);
    if (admin) console.log(`${name}\t${admin.users[0].toLowerCase()}`);
  }
  Deno.exit(0);
}

// Signs the account up (open-signup realms): a conflict means it is there, the
// realm's required consents are accepted as given, and a 202 asks for a verified
// address first (verifyEmail confirms it through the outbox).
const ensureAccount = async (client, realm, email, pw, outboxDb) => {
  const attempt = async (consents) => {
    try {
      await client.auth.signup({ email, password: pw, consents });
      return "ok";
    } catch (e) {
      if (e?.consents?.length) {
        return attempt(
          Object.fromEntries(e.consents.map((c) => [c.name, c.version])),
        );
      }
      return e?.status === 202 || e?.code === "verification_required" ? "verify" : "ok";
    }
  };
  if (await attempt() === "verify") await verifyEmail(client, realm, outboxDb, email);
};

// Creates the account as the realm's administrator (realms where signup is not
// open): `admin.users.create` makes it with the address already verified, and
// the realm's seeded role assignments apply at creation.
const createAsAdmin = async (admin, email, pw) => {
  try {
    await admin.admin.users.create({ email, password: pw });
    return true;
  } catch (e) {
    if (e?.status === 409 || /exist|taken|conflict/i.test(e?.message ?? "")) return true;
    warn(`admin couldn't create ${email}: ${e?.message ?? e}`);
    return false;
  }
};

// The storage keys the local MinIO knows (the compose files' MinIO user), for the private
// storage and for the public one.
const storageSecrets = {
  STORAGE_ACCESS_KEY: "backd-dev",
  STORAGE_SECRET_KEY: devPassword,
};
const publicStorageSecrets = {
  PUBLIC_STORAGE_ACCESS_KEY: "backd-dev",
  PUBLIC_STORAGE_SECRET_KEY: devPassword,
};

const out = [];
for (const [realm, s] of Object.entries(realms)) {
  const needsKeys = s.hasStorage || s.hasPublicStorage;
  if (!s.accounts.length && !needsKeys) continue;
  // The realm's administrator's session, for creating accounts where signup
  // isn't open and for setting the storage secrets: the first signed-in
  // account able to (a "users" admin), the first full one for the secrets.
  let usersAdmin = null, fullAdmin = null;
  let secretsDone = false;
  const pw = password(realm);
  for (const a of s.accounts) {
    const client = createClient({ url: api, realm });
    const account = {
      realm,
      email: a.email,
      password: pw,
      sign_in: a.sign_in ?? s.app,
      ...(a.note && { note: a.note }),
    };
    try {
      // Sign in first: an existing account answers at once, and no signup sends
      // an 'account-exists' mail. Only on a refusal does the account need
      // creating — or, on a verified-email realm, its address verifying.
      let keep;
      try {
        keep = await signIn(client, realm, a.email, pw);
      } catch (e) {
        if (s.verifyEmail && (e?.status === 403 || e?.code === "email_not_verified") &&
            await verifyEmail(client, realm, s.emailDb, a.email)) {
          // Signed up before but never verified: the mail's token is still good.
        } else if (e?.status === 401 || e?.code === "invalid_credentials") {
          if (s.signup === "open") {
            await ensureAccount(client, realm, a.email, pw, s.emailDb);
          } else if (usersAdmin) {
            await createAsAdmin(usersAdmin, a.email, pw);
          } else {
            warn(`no administrator of ${realm} signed in to create ${a.email}`);
          }
        } else {
          throw e;
        }
        keep = await signIn(client, realm, a.email, pw);
      }
      if (keep === null) {
        warn(`no session for ${a.email}: sign in by hand`);
      } else {
        if (a.admin === "full" && !fullAdmin) fullAdmin = client;
        if ((a.admin === "full" || s.roles[a.role]?.adminAreas?.includes("users")) && !usersAdmin) {
          usersAdmin = client;
        }
        if (needsKeys && !secretsDone && fullAdmin) {
          const wanted = { ...(s.hasStorage ? storageSecrets : {}), ...(s.hasPublicStorage ? publicStorageSecrets : {}) };
          for (const [k, v] of Object.entries(wanted)) {
            await fullAdmin.request({
              method: "PUT",
              path: ["_admin", "secrets", k],
              body: { value: v },
            });
          }
          secretsDone = true;
          note(`Storage keys set for the ${realm} realm (signed in as ${a.email}).`);
        }
        Object.assign(account, keep);
      }
    } catch (e) {
      warn(`${a.email}: ${e?.message ?? e}`);
    }
    out.push(account);
  }
  if (needsKeys && !secretsDone) {
    warn(`${realm}: storage keys not set — no full administrator's session`);
  }
}

console.log(JSON.stringify(
  { generated_at: new Date().toISOString(), accounts: out },
  null,
  2,
));
