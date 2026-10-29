import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { after, test } from "node:test";
import { deleteApp, initializeApp } from "firebase-admin/app";
import { getAuth } from "firebase-admin/auth";
import { getFirestore } from "firebase-admin/firestore";

const projectId = "demo-lanes";
const app = initializeApp({ projectId });
const db = getFirestore(app);
after(() => deleteApp(app));

test("Firestore keeps a written document", async () => {
  const ref = db.collection("suite").doc(randomUUID());
  await ref.set({ ok: true });
  assert.equal((await ref.get()).get("ok"), true);
});

test("Auth creates a user", async () => {
  const user = await getAuth(app).createUser({ email: `${randomUUID()}@example.com` });
  assert.ok(user.uid);
});

test("a message on demo-topic reaches the function", async () => {
  const marker = randomUUID();
  const data = Buffer.from(JSON.stringify({ marker })).toString("base64");
  const res = await fetch(`http://${process.env.PUBSUB_EMULATOR_HOST}/v1/projects/${projectId}/topics/demo-topic:publish`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ messages: [{ data }] }),
  });
  assert.equal(res.status, 200, await res.text());
  const deadline = Date.now() + 60_000;
  for (;;) {
    const found = await db.collection("events").where("marker", "==", marker).get();
    if (!found.empty) break;
    assert.ok(Date.now() < deadline, "no events document after 60 s");
    await new Promise((r) => setTimeout(r, 250));
  }
});
