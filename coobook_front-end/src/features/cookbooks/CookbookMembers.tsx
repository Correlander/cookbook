import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import type { Cookbook, CookbookInvitation, CookbookMember } from "../../apiModels";
import { errorMessage } from "../../apiModels";
import useModalFocus from "../../useModalFocus.ts";
import Icon from "../recipes/RecipeIcon.tsx";
import { createCookbookInvitation, getCookbookMembers, removeCookbookMember, revokeCookbookInvitation, updateCookbookMember } from "./cookbookApi.ts";

export default function CookbookMembers({
  book,
  onClose,
}: {
  book: Cookbook;
  onClose: () => void;
}) {
  const dialogRef = useRef<HTMLElement>(null);
  useModalFocus(dialogRef, onClose);
  const [members, setMembers] = useState<CookbookMember[]>([]),
    [invitations, setInvitations] = useState<CookbookInvitation[]>([]),
    [email, setEmail] = useState(""),
    [role, setRole] = useState<CookbookInvitation["role"]>("viewer"),
    [inviteURL, setInviteURL] = useState(""),
    [error, setError] = useState("");
  async function reload() {
    const data = await getCookbookMembers(book.id);
    setMembers(data.members);
    setInvitations(data.invitations);
  }
  useEffect(() => {
    let active = true;
    getCookbookMembers(book.id)
      .then((data) => {
        if (active) {
          setMembers(data.members);
          setInvitations(data.invitations);
        }
      })
      .catch((err: unknown) => {
        if (active)
          setError(errorMessage(err, "Could not load cookbook members."));
      });
    return () => {
      active = false;
    };
  }, [book.id]);
  async function invite(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    try {
      const item = await createCookbookInvitation(book.id, email, role);
      setInviteURL(
        `${window.location.origin}/?invite=${encodeURIComponent(item.token)}`,
      );
      setEmail("");
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not invite this person."));
    }
  }
  async function updateMember(
    userID: string,
    nextRole: CookbookInvitation["role"],
  ) {
    try {
      await updateCookbookMember(book.id, userID, nextRole);
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not update this member."));
    }
  }
  async function removeMember(userID: string) {
    try {
      await removeCookbookMember(book.id, userID);
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not remove this member."));
    }
  }
  async function revoke(id: string) {
    try {
      await revokeCookbookInvitation(book.id, id);
      await reload();
    } catch (err: unknown) {
      setError(errorMessage(err, "Could not revoke this invitation."));
    }
  }
  return (
    <div
      className="dialog-backdrop"
      role="presentation"
      onMouseDown={(event) => event.target === event.currentTarget && onClose()}
    >
      <section
        className="signin-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="members-title"
        ref={dialogRef}
        tabIndex={-1}
      >
        <button
          className="icon-button dialog-close"
          aria-label="Close members"
          onClick={onClose}
        >
          <Icon name="close" />
        </button>
        <span className="eyebrow">{book.title}</span>
        <h2 id="members-title">Cookbook members</h2>
        <div className="passkey-list">
          {members.map((member) => (
            <div className="passkey-row" key={member.user_id}>
              <span>
                {member.display_name}
                <small>
                  {member.email} · {member.role}
                </small>
              </span>
              {member.role !== "owner" && (
                <>
                  <select
                    aria-label={`Role for ${member.display_name}`}
                    value={member.role}
                    onChange={(event) =>
                      updateMember(
                        member.user_id,
                        event.target.value as CookbookInvitation["role"],
                      )
                    }
                  >
                    <option value="viewer">Viewer</option>
                    <option value="editor">Editor</option>
                  </select>
                  <button
                    type="button"
                    className="text-link"
                    onClick={() => removeMember(member.user_id)}
                  >
                    Remove
                  </button>
                </>
              )}
            </div>
          ))}
        </div>
        <form onSubmit={invite}>
          <label htmlFor="member-email">Invite by email</label>
          <input
            id="member-email"
            type="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
            required
          />
          <label htmlFor="member-role">Role</label>
          <select
            id="member-role"
            value={role}
            onChange={(event) =>
              setRole(event.target.value as CookbookInvitation["role"])
            }
          >
            <option value="viewer">Viewer</option>
            <option value="editor">Editor</option>
          </select>
          <button className="button button--green" type="submit">
            Create invitation
          </button>
        </form>
        {inviteURL && (
          <p role="status">
            Invitation link (send securely): <a href={inviteURL}>{inviteURL}</a>
          </p>
        )}
        {invitations.map((item) => (
          <div className="passkey-row" key={item.id}>
            <span>
              {item.email}
              <small>
                {item.role} · expires{" "}
                {new Date(item.expires_at).toLocaleDateString()}
              </small>
            </span>
            <button
              type="button"
              className="text-link"
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
    </div>
  );
}
