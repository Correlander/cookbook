import { useEffect, useState } from "react";
import type { CreatorProfile } from "../../apiModels";
import {
  CreatorProfileApiError,
  getSelfCreatorProfile,
  saveSelfCreatorProfile,
} from "./creatorProfileApi.ts";

const NAME_MAX_SCALARS = 80;
const BIO_MAX_BYTES = 1000;

export default function CreatorProfileEditor({
  actorID,
  onSessionExpired,
}: {
  actorID: string;
  onSessionExpired: () => void;
}) {
  const [profile, setProfile] = useState<CreatorProfile | null>(null);
  const [name, setName] = useState("");
  const [bio, setBio] = useState("");
  const [visibility, setVisibility] =
    useState<CreatorProfile["visibility"]>("private");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState(false);
  const [retry, setRetry] = useState(0);
  const [sessionExpired, setSessionExpired] = useState(false);

  useEffect(() => {
    let active = true;
    setLoading(true);
    setError("");
    setProfile(null);
    setSessionExpired(false);
    void getSelfCreatorProfile()
      .then(({ profile: current }) => {
        if (!active) return;
        setProfile(current);
        setName(current?.public_name ?? "");
        setBio(current?.bio ?? "");
        setVisibility(current?.visibility ?? "private");
      })
      .catch((cause: unknown) => {
        if (!active) return;
        if (cause instanceof CreatorProfileApiError && cause.status === 401)
          setSessionExpired(true);
        else if (
          cause instanceof CreatorProfileApiError &&
          cause.status === 404
        )
          setError(
            "This Cookbook server does not support creator profiles yet.",
          );
        else setError("Could not load your creator profile. Try again.");
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [actorID, retry]);

  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError("");
    setSaved(false);
    const normalizedName = name.trim();
    const scalarCount = Array.from(normalizedName).length;
    if (
      visibility === "instance" &&
      (scalarCount < 1 ||
        scalarCount > NAME_MAX_SCALARS ||
        new TextEncoder().encode(normalizedName).length > 320)
    ) {
      setError(
        "For this instance, enter a profile name of 1–80 characters (up to 320 UTF-8 bytes).",
      );
      return;
    }
    if (new TextEncoder().encode(bio).length > BIO_MAX_BYTES) {
      setError("Bio must be 1,000 UTF-8 bytes or fewer.");
      return;
    }
    setSaving(true);
    try {
      const result = await saveSelfCreatorProfile({
        public_name: normalizedName,
        bio,
        visibility,
      });
      if (!result.profile)
        throw new Error("Creator profile save returned no profile.");
      setProfile(result.profile);
      setName(result.profile.public_name);
      setBio(result.profile.bio);
      setVisibility(result.profile.visibility);
      setSaved(true);
    } catch (cause) {
      const apiError = cause instanceof CreatorProfileApiError ? cause : null;
      if (apiError?.status === 401) setSessionExpired(true);
      setError(
        apiError?.status === 400
          ? "The profile details are not valid. Check the name and bio limits."
          : "Could not save your creator profile. Your changes are still here; try again.",
      );
    } finally {
      setSaving(false);
    }
  }

  return (
    <section
      className="cookbook-dashboard"
      aria-labelledby="creator-profile-editor-title"
    >
      <h2 id="creator-profile-editor-title">Creator profile details</h2>
      {loading ? (
        <p role="status">Loading creator profile…</p>
      ) : sessionExpired ? (
        <div role="alert">
          <p>
            Your session could not access the creator profile. Retry your
            account session before continuing.
          </p>
          <button type="button" onClick={onSessionExpired}>
            Retry session
          </button>
        </div>
      ) : error && !profile && !name && !bio ? (
        <div role="alert">
          <p>{error}</p>
          <button type="button" onClick={() => setRetry((value) => value + 1)}>
            Retry profile
          </button>
        </div>
      ) : (
        <>
          <p>
            Creator credit is self-asserted. Profile visibility is separate from
            recipe visibility and does not grant access to recipes or cookbooks.
          </p>
          <form className="creator-profile-form" onSubmit={submit}>
            <label htmlFor="creator-public-name">Public name
            <input
              id="creator-public-name"
              value={name}
              maxLength={320}
              onChange={(event) => setName(event.target.value)}
            /></label>
            <label htmlFor="creator-bio">Bio
            <textarea
              id="creator-bio"
              value={bio}
              onChange={(event) => setBio(event.target.value)}
            /></label>
            <label htmlFor="creator-visibility">Profile visibility
            <select
              id="creator-visibility"
              value={visibility}
              onChange={(event) =>
                setVisibility(
                  event.target.value as CreatorProfile["visibility"],
                )
              }
            >
              <option value="private">Private</option>
              <option value="instance">This instance</option>
            </select></label>
            <p className="dialog-note">
              {visibility === "private"
                ? "Only this account can view the profile and its readable attributed items."
                : "Signed-in people on this same Cookbook instance may view the profile. Each listed recipe or cookbook still follows its own access rules."}
            </p>
            {error && (
              <p role="alert" className="form-message">
                {error}
              </p>
            )}
            {saved && <p role="status">Creator profile saved.</p>}
            <div className="creator-profile-actions"><button className="button button--green" type="submit" disabled={saving}>{saving ? "Saving…" : "Save profile"}</button>
              {profile && <a className="button button--outline" href={`/creator?id=${encodeURIComponent(profile.id)}`}>{profile.visibility === "instance" ? "Open profile preview" : "Preview private profile"}</a>}
            </div>
          </form>
        </>
      )}
    </section>
  );
}
