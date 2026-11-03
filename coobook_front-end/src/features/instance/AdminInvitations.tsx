import { useEffect, useState } from "react";
import { errorMessage } from "../../apiModels";
import type { AdminInvitation, AdminInvitationCreated } from "../../apiModels";

export default function AdminInvitations() {
  const [authorized, setAuthorized] = useState<boolean | null>(null),
    [items, setItems] = useState<AdminInvitation[]>([]),
    [email, setEmail] = useState(""),
    [error, setError] = useState(""),
    [inviteURL, setInviteURL] = useState("");
  async function reload() {
    const response = await fetch("/api/admin/invitations");
    if (response.status === 403) {
      setAuthorized(false);
      return;
    }
    if (!response.ok) throw new Error("Could not load invitations.");
    setItems((await response.json()) as AdminInvitation[]);
    setAuthorized(true);
  }
  useEffect(() => {
    reload().catch((err: unknown) =>
      setError(errorMessage(err, "Could not load invitations.")),
    );
  }, []);
  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    try {
      const response = await fetch("/api/admin/invitations", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email }),
      });
      if (!response.ok) throw new Error("Could not create an invitation.");
      const invite = (await response.json()) as AdminInvitationCreated;
      setInviteURL(
        `${window.location.origin}/?invite=${encodeURIComponent(invite.token)}`,
      );
      setEmail("");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not create an invitation."));
    }
  }
  async function revoke(id: string) {
    try {
      const response = await fetch(
        `/api/admin/invitations?id=${encodeURIComponent(id)}`,
        { method: "DELETE" },
      );
      if (!response.ok) throw new Error("Could not revoke this invitation.");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not revoke this invitation."));
    }
  }
  if (authorized === false) return null;
  return (
    <section className="cookbook-dashboard">
      <div className="section-heading">
        <div>
          <span className="eyebrow">Instance access</span>
          <h2>Invitations</h2>
        </div>
      </div>
      <form className="inline-invite" onSubmit={create}>
        <label htmlFor="invite-email">Invite an email address</label>
        <input
          id="invite-email"
          type="email"
          value={email}
          onChange={(event) => setEmail(event.target.value)}
          required
        />
        <button className="button button--green" type="submit">
          Create invitation
        </button>
      </form>
      {inviteURL && (
        <p role="status">
          Invitation link (copy and send securely):{" "}
          <a href={inviteURL}>{inviteURL}</a>
        </p>
      )}
      {items.map((item) => (
        <div className="passkey-row" key={item.id}>
          <span>
            {item.email}
            <small>
              Expires {new Date(item.expires_at).toLocaleDateString()}
            </small>
          </span>
          <button
            className="text-link"
            type="button"
            onClick={() => revoke(item.id)}
          >
            Revoke
          </button>
        </div>
      ))}
      {error && (
        <p className="form-message" role="alert">
          {error}
        </p>
      )}
    </section>
  );
}
