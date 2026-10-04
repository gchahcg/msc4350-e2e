// Copyright (c) 2026 gchahcg
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at http://mozilla.org/MPL/2.0/.

// Drives Element Web in headless Chromium: logs in, has the fake bridge send a message from a new ghost, joins the
// room and reads the shield state Element's crypto reports for that message, first right after it arrived and again
// after a page reload (which checks that impersonatable devices survive a restart of the client).
//
// Prints one JSON line: {"first": {...}, "afterReload": {...}} where each value is {shieldColour, shieldReason}
// (shieldColour 0 means no warning). Environment: ELEMENT_URL, INJECT_URL, ELEMENT_USER, ELEMENT_PASSWORD, GHOST.

import fs from "node:fs";
import os from "node:os";
import path from "node:path";

import { chromium } from "playwright-core";

const env = (name, fallback) => process.env[name] ?? fallback;
const elementUrl = env("ELEMENT_URL", "https://localhost:18443");
const injectUrl = env("INJECT_URL", "http://127.0.0.1:29400");
const user = env("ELEMENT_USER", "alice");
const password = env("ELEMENT_PASSWORD", "alicepw");
const ghost = env("GHOST", `ghost${Date.now() % 1_000_000}`);
const shotDir = env("SHOT_DIR", "");

function findChromium() {
  if (process.env.CHROMIUM_PATH) return process.env.CHROMIUM_PATH;
  const base = path.join(os.homedir(), ".cache/ms-playwright");
  for (const dir of fs.existsSync(base) ? fs.readdirSync(base).sort().reverse() : []) {
    if (!dir.startsWith("chromium-")) continue;
    for (const sub of fs.readdirSync(path.join(base, dir))) {
      const candidate = path.join(base, dir, sub, "chrome");
      if (fs.existsSync(candidate)) return candidate;
    }
  }
  throw new Error("no Chromium found, set CHROMIUM_PATH (for example from `npx playwright install chromium`)");
}

const browser = await chromium.launch({ executablePath: findChromium(), headless: true, args: ["--no-sandbox"] });
const context = await browser.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1280, height: 900 } });
const page = await context.newPage();
const shot = async (name) => shotDir && (await page.screenshot({ path: path.join(shotDir, `${name}.png`) }));
const fail = async (message) => {
  await shot("failure").catch(() => {});
  await browser.close();
  console.error(`element check failed: ${message}`);
  process.exit(1);
};

async function waitForClient() {
  await page.waitForFunction(() => !!window.mxMatrixClientPeg?.get?.()?.getCrypto?.() && window.mxMatrixClientPeg.get().isInitialSyncComplete(), null, { timeout: 90_000 });
}

// Read the shield info for the first message from the ghost in the room, once it is decrypted.
async function readShield(roomId, ghostId) {
  return page.evaluate(
    async ({ roomId, ghostId }) => {
      const client = window.mxMatrixClientPeg.get();
      const deadline = Date.now() + 60_000;
      while (Date.now() < deadline) {
        const room = client.getRoom(roomId);
        const events = room?.getLiveTimeline().getEvents().filter((e) => e.getSender() === ghostId) ?? [];
        const event = events[events.length - 1];
        if (event) {
          await client.decryptEventIfNeeded(event);
          if (!event.isDecryptionFailure() && event.getContent().body) {
            const info = await client.getCrypto().getEncryptionInfoForEvent(event);
            return { body: event.getContent().body, shieldColour: info?.shieldColour, shieldReason: info?.shieldReason ?? null };
          }
        }
        await new Promise((r) => setTimeout(r, 500));
      }
      throw new Error("the ghost's message was never decrypted");
    },
    { roomId, ghostId },
  );
}

try {
  await page.goto(`${elementUrl}/#/welcome`);
  await page.getByRole("link", { name: "Sign in" }).or(page.getByRole("button", { name: "Sign in" })).first().click();
  await page.fill("#mx_LoginForm_username", user);
  await page.fill("#mx_LoginForm_password", password);
  await page.locator("form").getByRole("button", { name: "Sign in" }).click();
  await waitForClient();
  await shot("1-logged-in");

  const response = await fetch(`${injectUrl}/inject`, {
    method: "POST",
    body: JSON.stringify({ user_mxid: `@${user}:test.local`, ghost, text: "hello from the ghost" }),
  });
  if (!response.ok) await fail(`inject failed: ${response.status} ${await response.text()}`);
  const { room_id: roomId, ghost_mxid: ghostId } = await response.json();

  // Join the invite the bridge sent.
  await page.evaluate(async (roomId) => {
    const client = window.mxMatrixClientPeg.get();
    const deadline = Date.now() + 60_000;
    while (Date.now() < deadline) {
      if (client.getRoom(roomId)?.getMyMembership() === "invite") {
        await client.joinRoom(roomId);
        return;
      }
      await new Promise((r) => setTimeout(r, 500));
    }
    throw new Error("no invite to the bridged room arrived");
  }, roomId);

  // Give the key query for the room's members time to finish, then read the state.
  await page.waitForTimeout(4000);
  const first = await readShield(roomId, ghostId);
  await shot("2-first");

  await page.reload();
  await waitForClient();
  await page.waitForTimeout(3000);
  const afterReload = await readShield(roomId, ghostId);
  await shot("3-after-reload");

  console.log(JSON.stringify({ first, afterReload }));
  await browser.close();
} catch (e) {
  await fail(String(e?.stack ?? e));
}
