import type { CreatorAttribution } from "../../apiModels";

type CreatorCreditsProps = {
  attributions?: CreatorAttribution[];
  resourceKind: "recipe" | "cookbook";
  variant?: "compact" | "detail";
  currentUserID?: string | null;
};

function roleLabel(
  role: string,
  resourceKind: CreatorCreditsProps["resourceKind"],
): string {
  switch (role) {
    case "creator":
      return resourceKind === "cookbook" ? "Collection creator" : "Creator";
    case "contributor":
      return "Contributor";
    case "adapter":
      return "Adapter";
    default:
      return "Unverified relationship";
  }
}

function provenanceLabel(provenance: string): string {
  switch (provenance) {
    case "self_asserted_at_creation":
      return "Self-reported when added";
    case "local_record_creation":
      return "Recorded when the cookbook was created";
    case "adapter_source":
      return "Recorded as a source adapter";
    case "portable_claim":
      return "Imported claim";
    default:
      return "Unverified provenance";
  }
}

function subjectText(attribution: CreatorAttribution): string {
  if (attribution.subject_kind === "external_claim") {
    const name = attribution.name_claim?.trim();
    return name
      ? `Unverified creator claim: ${name}`
      : "Unverified creator claim";
  }
  const name = attribution.profile.public_name.trim();
  if (name) return name;
  return attribution.profile.visibility === "private"
    ? "Your creator profile (name not set)"
    : "Creator profile (name not set)";
}

function CreatorCreditIdentity({
  attribution,
  currentUserID,
}: {
  attribution: CreatorAttribution;
  currentUserID: string | null;
}) {
  if (
    attribution.subject_kind === "local" &&
    currentUserID &&
    attribution.profile.public_name.trim().length > 0
  ) {
    return (
      <a href={`/creator?id=${encodeURIComponent(attribution.profile.id)}`}>
        {attribution.profile.public_name.trim()}
      </a>
    );
  }
  return <span>{subjectText(attribution)}</span>;
}

export default function CreatorCredits({
  attributions,
  resourceKind,
  variant = "detail",
  currentUserID = null,
}: CreatorCreditsProps) {
  if (!attributions?.length) return null;
  const label =
    resourceKind === "cookbook" ? "Cookbook creator credits" : "Recipe creator credits";

  if (variant === "compact") {
    return (
      <span className="creator-credits creator-credits--compact">
        {attributions.map((attribution, index) => (
          <span className="creator-credits__item" key={`${attribution.subject_kind}-${attribution.role}-${index}`}>
            <span>{subjectText(attribution)}</span>
            <span>
              {roleLabel(attribution.role, resourceKind)} · {provenanceLabel(attribution.provenance)}
            </span>
          </span>
        ))}
      </span>
    );
  }

  const Heading = resourceKind === "cookbook" ? "h2" : "h3";
  return (
    <section className="creator-credits" aria-label={label}>
      <Heading>{resourceKind === "cookbook" ? "Creator credits" : "Recipe creator credits"}</Heading>
      <ul>
        {attributions.map((attribution, index) => (
          <li className="creator-credits__item" key={`${attribution.subject_kind}-${attribution.role}-${index}`}>
            <CreatorCreditIdentity
              attribution={attribution}
              currentUserID={currentUserID}
            />
            <span>
              {roleLabel(attribution.role, resourceKind)} · {provenanceLabel(attribution.provenance)}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}
