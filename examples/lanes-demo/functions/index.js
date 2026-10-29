const { onMessagePublished } = require("firebase-functions/v2/pubsub");
const { initializeApp } = require("firebase-admin/app");
const { getFirestore } = require("firebase-admin/firestore");

if (process.env.DEMO_SLOW_LOAD) {
  const until = Date.now() + 35_000;
  while (Date.now() < until);
}

initializeApp();

exports.onDemoTopic = onMessagePublished("demo-topic", async (event) => {
  const message = event.data.message.json ?? {};
  await getFirestore()
    .collection("events")
    .doc(event.id)
    .set({ ...message, receivedAt: new Date().toISOString() });
});
