import type { RecipeCreatorAttribution } from "../../apiModels";

type AttributionKind = RecipeCreatorAttribution["kind"];

interface CreatorAttributionFieldsProps {
  resourceKind: "recipe" | "course" | "menu";
  choice: AttributionKind | "";
  name: string;
  onChoiceChange: (choice: AttributionKind | "") => void;
  onNameChange: (name: string) => void;
}

export default function CreatorAttributionFields({
  resourceKind,
  choice,
  name,
  onChoiceChange,
  onNameChange,
}: CreatorAttributionFieldsProps) {
  const resource = resourceKind;
  const id = resourceKind === "recipe" ? "creator-attribution" : `${resourceKind}-creator-attribution`;
  const nameID = resourceKind === "recipe" ? "creator-name-claim" : `${resourceKind}-creator-name-claim`;
  const requiresName = resourceKind === "course" || resourceKind === "menu";
  return (
    <>
      <legend>Who created this {resource}? <span aria-hidden="true">*</span></legend>
      <label htmlFor={id}>Creator credit</label>
      <select
        id={id}
        value={choice}
        onChange={(event) => onChoiceChange(event.target.value as AttributionKind | "")}
        required
      >
        <option value="">Choose a creator credit</option>
        <option value="local_profile">I created this {resource}</option>
        <option value="name_claim">Someone else created this {resource}</option>
        <option value="unknown">I don’t know who created this {resource}</option>
      </select>
      {choice === "name_claim" && <>
        <label htmlFor={nameID}>Creator name (unverified)</label>
        <input
          id={nameID}
          value={name}
          maxLength={320}
          required={requiresName}
          onChange={(event) => onNameChange(event.target.value)}
        />
        <p className="dialog-note">{resourceKind === "menu"
          ? "This is unverified text, not a linked profile. It is visible only to people allowed to read this private menu."
          : `This is unverified text, not a linked profile. A public ${resource} can expose this name. It follows the ${resource}’s visibility.`}</p>
      </>}
      {choice === "local_profile" && <p className="dialog-note">This is a self-asserted credit. A private profile with an empty name is created if needed. This does not publish your profile or {resourceKind === "menu" ? "private menu" : resource}.</p>}
      {choice === "unknown" && <p className="dialog-note">No creator name or typed creator relation will be recorded.</p>}
      <p className="dialog-note">Creator credit and profile visibility are separate from {resourceKind === "menu" ? "private menu access" : `${resource} visibility`}.</p>
    </>
  );
}
