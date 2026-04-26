// Package app holds build-time configuration for the atelierd daemon.
//
// All values here are public — same as those shipped in the valian-dashboards
// frontend bundle. They live as Go consts (rather than ldflags-stamped vars)
// because they don't need per-build override; only the version is stamped.
package app

const (
	// FirebaseProjectID identifies the Google Cloud / Firebase project the
	// daemon writes to.
	FirebaseProjectID = "valian-dashboards"

	// FirebaseAPIKey is the public Web API key used by the Firebase Auth REST
	// endpoints. Same value the frontend ships in its bundle.
	FirebaseAPIKey = "AIzaSyB8orDLOWG37xGvjH2tClhkSelOP4xIy7Y"

	// FirebaseAuthDomain is the Firebase Hosting / Auth domain. The dashboard
	// is served from the same hostname.
	FirebaseAuthDomain = "valian-dashboards.firebaseapp.com"

	// FunctionsRegion is the GCP region where the api callable is deployed.
	FunctionsRegion = "northamerica-northeast1"

	// CallableBaseURL is the base URL of the single `api` Cloud Function.
	// All routed calls (createDeviceCode, exchangeDeviceCode, …) hit this URL
	// with a Firebase callable wrapper: { "data": { "type": "<route>", "value": ... } }.
	CallableBaseURL = "https://" + FunctionsRegion + "-" + FirebaseProjectID + ".cloudfunctions.net"

	// FirestoreBaseURL is the Firestore REST endpoint base.
	FirestoreBaseURL = "https://firestore.googleapis.com/v1/projects/" + FirebaseProjectID + "/databases/(default)/documents"

	// SecureTokenURL is the Firebase Auth token-refresh endpoint.
	SecureTokenURL = "https://securetoken.googleapis.com/v1/token?key=" + FirebaseAPIKey

	// IdentityToolkitURL is the Firebase Auth REST base.
	IdentityToolkitURL = "https://identitytoolkit.googleapis.com/v1"
)

// DashboardConnectMachineURL returns the URL the user opens to enter the
// device-link code in the dashboard.
func DashboardConnectMachineURL(code string) string {
	return "https://" + FirebaseAuthDomain + "/connect-machine?code=" + code
}

// EventsCollectionURL returns the Firestore REST URL for an /events doc.
func EventsCollectionURL(ulid string) string {
	return FirestoreBaseURL + "/events/" + ulid
}

// UserDocumentURL returns the Firestore REST URL for a /users doc.
func UserDocumentURL(uid string) string {
	return FirestoreBaseURL + "/users/" + uid
}

// CommitURL returns the Firestore REST :commit endpoint, which supports
// transforms (used for serverTimestamp on heartbeat).
const CommitURL = "https://firestore.googleapis.com/v1/projects/" + FirebaseProjectID + "/databases/(default)/documents:commit"
