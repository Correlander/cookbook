export interface ApiFailure {
  error?: string;
  message?: string;
}

export interface AuthenticatedUser {
  id: string;
  email: string;
  display_name: string;
  is_admin: boolean;
}

export interface AdminUser extends AuthenticatedUser {
  created_at: string;
}

export interface InstanceAuthority {
  is_instance_owner: boolean;
  is_admin: boolean;
  owner_claimed: boolean;
  can_manage_instance_roles: boolean;
  can_manage_instance_settings?: boolean;
}

export type OwnerTransferDirection = "incoming" | "outgoing";

export interface OwnerTransfer {
  id: string;
  direction: OwnerTransferDirection;
  state: "pending";
  created_at: string;
  expires_at: string;
  counterparty_display_name: string;
}

export interface OwnerTransferReadResponse {
  transfers: OwnerTransfer[];
}

export interface OwnerTransferCreatePayload {
  target_user_id: string;
}

export interface Passkey {
  id: string;
  name: string;
  created_at: string;
  last_used_at?: string | null;
}

export interface BrowserSession {
  id: string;
  current: boolean;
  user_agent?: string | null;
  created_at: string;
  expires_at: string;
}

export type RegistrationPolicy = "invite_only" | "open" | "disabled";

export interface AuthMethods {
  email_link: boolean;
  passkey: boolean;
}

export interface PublicAuthConfig {
  auth_methods: AuthMethods;
  registration_policy: RegistrationPolicy;
  public_instance: boolean;
  courses_enabled: boolean;
  course_creator_attribution?: boolean;
}

export interface AdminSettingsModel {
  public_instance: boolean;
  courses_enabled: boolean;
  registration_policy: RegistrationPolicy;
  revision_retention_days: number;
  audit_retention_days: number;
  federation_enabled: boolean;
  federation_origin: string;
  public_cookbooks: number;
  public_recipes: number;
  public_courses: number;
}

export interface Course {
  id: string;
  title: string;
  owner_id?: string;
  owner_name: string;
  description: string;
  visibility: "private" | "public";
  lesson_count: number;
  created_at: string;
  updated_at: string;
  creator_name?: string;
  creator_attributions?: CreatorAttribution[];
}

export type CourseCreatorAttribution =
  | { kind: "local_profile" }
  | { kind: "name_claim"; name: string }
  | { kind: "unknown" };

export interface CourseLesson {
  id: string;
  course_id: string;
  title: string;
  content: string;
  recipe_id?: string;
  position: number;
  created_at: string;
  updated_at: string;
}

export type MenuCreatorChoice =
  | { kind: "local_profile" }
  | { kind: "name_claim"; name: string }
  | { kind: "unknown" };

export interface MenuRecipeSummary {
  id: string;
  title: string;
}

export interface MenuEntry {
  id: string;
  position: number;
  label: string;
  note: string;
  serving_note: string;
  available: true;
  recipe: MenuRecipeSummary;
}

export interface UnavailableMenuEntry {
  id: string;
  position: number;
  label: string;
  note: string;
  serving_note: string;
  available: false;
}

export type MenuEntryView = MenuEntry | UnavailableMenuEntry;

export interface PrivateMenu {
  id: string;
  title: string;
  note: string;
  creator_name?: string;
  version: number;
  created_at: string;
  updated_at: string;
  creator_attributions?: CreatorAttribution[];
  entries: MenuEntryView[];
}

export interface PrivateMenuPage {
  menus: PrivateMenu[];
  total: number;
  limit: number;
  offset: number;
}

export interface RecipeSummary {
  id: string;
  title: string;
  [key: string]: unknown;
}

export interface RecipeStep {
  text: string;
}

export interface RecipeIngredient {
  name: string;
  quantity?: string;
  notes?: string;
}

export interface PublicRecipeContent {
  title: string;
  description: string;
  ingredients: RecipeIngredient[];
  steps: RecipeStep[];
  servings: number;
  prep_time_minutes: number;
  cook_time_minutes: number;
  tags: string[];
}

export interface PublicRecipeDetail extends PublicRecipeContent {
  id: string;
  creator_name: string;
  version: number;
  created_at: string;
  updated_at: string;
  image?: string;
}

export interface PublicCookbookRecipe extends PublicRecipeDetail {
  visibility: "public";
}

export interface RecipeBookmarkTarget {
  local_recipe_id?: string;
  remote_object_uri?: string;
}

export interface RemoteRecipeReadView {
  object_uri: string;
  source_origin: string;
  source_recipe_id: string;
  creator_name: string;
  recipe: PublicRecipeContent;
  sequence: number;
  updated_at: string;
}

export interface PrivateSnapshotRecipeReadView {
  object_uri: string;
  source_origin: string;
  source_recipe_id: string;
  creator_name: string;
  recipe: PublicRecipeContent;
  sequence?: number;
  updated_at: string;
  received_at?: string;
  version_id?: string;
  provenance_state?: "publisher_verified" | "user_imported_unverified";
  publisher_retention_claim?: string;
  source_state?: string;
}

export type RecipeBookmarkSaveState =
  | "reference"
  | "snapshot"
  | "restricted"
  | "removed";

export interface RecipeBookmarkView {
  id: string;
  target_kind: "local" | "remote";
  target?: RecipeBookmarkTarget;
  availability: "available" | "unavailable";
  local_recipe?: Recipe;
  remote_recipe?: RemoteRecipeReadView;
  snapshot_recipe?: PrivateSnapshotRecipeReadView;
  save_state?: RecipeBookmarkSaveState;
  version_id?: string;
  source_state?: string;
  provenance_state?: "publisher_verified" | "user_imported_unverified";
  publisher_retention_claim?: string;
  moderation_reason?: "legal" | "security" | "privacy" | "policy";
  moderated_at?: string;
  moderation_outcome?: "restricted" | "removed";
  snapshot_available?: boolean;
  can_capture_snapshot?: boolean;
  saved?: boolean;
  created_at: string;
}

export interface RecipeBookmarkPage {
  items: RecipeBookmarkView[];
  total: number;
  limit: number;
  offset: number;
}

export interface CookbookBookmarkView {
  id: string;
  target_kind: "local_cookbook";
  target?: { local_cookbook_id: string };
  availability: "available" | "unavailable";
  cookbook?: Cookbook;
  created_at: string;
}

export interface CookbookBookmarkPage {
  items: CookbookBookmarkView[];
  total: number;
  limit: number;
  offset: number;
}

export type LibraryType = "recipes" | "cookbooks" | "courses";
export type LibraryRelationship = "owned" | "shared" | "saved";
export type LibraryRecipeRelationship = LibraryRelationship | "in_owned_cookbook";

export interface LibraryRecipeCookbookContext {
  id: string;
  membership_role?: string;
}

export interface LibraryRecipeItem {
  availability: "available" | "unavailable";
  target_kind: "local" | "remote";
  bookmark_id?: string;
  relationships?: LibraryRecipeRelationship[];
  membership_role?: string;
  in_owned_cookbook?: boolean;
  cookbook_contexts?: LibraryRecipeCookbookContext[];
  recipe?: Recipe;
  remote_recipe?: RemoteRecipeReadView;
  snapshot_recipe?: PrivateSnapshotRecipeReadView;
  save_state?: RecipeBookmarkSaveState;
  version_id?: string;
  source_state?: string;
  provenance_state?: "publisher_verified" | "user_imported_unverified";
  publisher_retention_claim?: string;
  moderation_reason?: "legal" | "security" | "privacy" | "policy";
  moderated_at?: string;
  moderation_outcome?: "restricted" | "removed";
  created_at?: string;
}

export interface LibraryCookbookItem {
  availability: "available" | "unavailable";
  bookmark_id?: string;
  relationships?: LibraryRelationship[];
  membership_role?: string;
  cookbook?: Cookbook;
  created_at?: string;
}

export type LibraryCourseRelationship = "owned" | "saved";

export type LibraryCourseItem =
  | { availability: "available"; relationships: LibraryCourseRelationship[]; bookmark_id?: string; course: Course }
  | { availability: "unavailable"; relationships: ["saved"]; bookmark_id: string; created_at: string };

export interface LibraryPage<T> {
  items: T[];
  total: number;
  limit: number;
  offset: number;
}

export type RecipeBookmarkStatus =
  | { saved: false }
  | {
      saved: true;
      bookmark_id: string;
      can_capture_snapshot?: boolean;
      bookmark?: RecipeBookmarkView;
    };

export type CookbookBookmarkStatus =
  { saved: false } | { saved: true; bookmark_id: string };

export type CourseBookmarkStatus = { saved: false } | { saved: true; bookmark_id: string };

export interface RemoteRecipeListItem {
  object_id: string;
  source_origin: string;
  creator_name: string;
  sequence: number;
  recipe: PublicRecipeContent;
  updated_at: string;
}

export interface CookingRecipe {
  servings: number;
  title: string;
  steps: RecipeStep[];
  ingredients: RecipeIngredient[];
}

export interface FederationPeer {
  id: string;
  name: string;
  origin: string;
  enabled: boolean;
  public_key: string;
}

export interface FederationIdentity {
  origin: string;
  key_id: string;
  public_key: string;
  pending_key_id?: string;
  pending_public_key?: string;
}

export interface FederationPeerKey {
  key_id: string;
  public_key: string;
  status: string;
  created_at: string;
  revoked_at?: string;
}

export interface FederationPeerAdmin {
  origin: string;
  enabled: boolean;
  exchange_profile?: "v1" | "exchange-v2-retention";
  created_at: string;
  updated_at: string;
  keys: FederationPeerKey[];
}

export interface FederationDelivery {
  id: string;
  peer_origin: string;
  event_id: string;
  object_id: string;
  sequence: number;
  attempts: number;
  next_attempt: number;
  last_error: string;
  created_at: string;
  payload?: unknown;
}

export interface CookbookSummary {
  id: string;
  title: string;
  description?: string;
  visibility?: "private" | "public" | "unlisted";
  owner_id?: string;
  recipe_count?: number;
  created_at?: string;
  updated_at?: string;
}

export interface PublicCookbook {
  id: string;
  title: string;
  description: string;
  recipe_count: number;
  created_at: string;
  updated_at: string;
}

export type CreatorAttributionRole = "creator" | "contributor" | "adapter";

export interface LocalCreatorAttribution {
  subject_kind: "local";
  profile: {
    id: string;
    public_name: string;
    visibility: "private" | "instance";
  };
  role: CreatorAttributionRole;
  provenance: string;
}

export interface ExternalCreatorAttribution {
  subject_kind: "external_claim";
  name_claim?: string;
  role: CreatorAttributionRole;
  provenance: string;
}

export type CreatorAttribution =
  LocalCreatorAttribution | ExternalCreatorAttribution;

export interface Recipe {
  id: string;
  owner_id?: string;
  creator_name: string;
  creator_attributions?: CreatorAttribution[];
  source_recipe_id?: string;
  source_origin?: string;
  sequence?: number;
  title: string;
  description: string;
  ingredients: RecipeIngredient[];
  steps: Array<{ text: string }>;
  servings: number;
  prep_time_minutes: number;
  cook_time_minutes: number;
  tags: string[];
  visibility: "private" | "unlisted" | "public";
  edit_policy?: string;
  can_edit: boolean;
  can_delete: boolean;
  can_manage_sharing?: boolean;
  version: number;
  cookbook_ids?: string[];
  created_at: string;
  updated_at: string;
  deleted_at?: string;
  image?: string;
}

export interface RecipeWritePayload {
  title: string;
  description: string;
  servings: number;
  prep_time_minutes: number;
  cook_time_minutes: number;
  tags: string[];
  visibility: Recipe["visibility"];
  edit_policy: string;
  cookbook_ids?: string[];
  ingredients: RecipeIngredient[];
  steps: Array<{ text: string }>;
  version?: number;
  confirm_public_access?: true;
}

export interface CreatorProfile {
  id: string;
  public_name: string;
  bio: string;
  visibility: "private" | "instance";
  created_at: string;
  updated_at: string;
}

export interface CreatorProfileEnvelope {
  profile: CreatorProfile | null;
}

export type RecipeCreatorAttribution =
  | { kind: "local_profile" }
  | { kind: "name_claim"; name: string }
  | { kind: "unknown" };

export interface CreatorRecipeItem {
  recipe: Recipe;
  role: string;
  provenance: string;
}

export interface CreatorCookbookItem {
  cookbook: Pick<
    Cookbook,
    "id" | "title" | "description" | "visibility" | "created_at" | "updated_at"
  >;
  role: string;
  provenance: string;
}

export interface CreatorCourseItem {
  course: Pick<Course, "id" | "title">;
  role: "creator";
  provenance: "self_asserted_at_creation";
}

export interface CreatorProfileList<T> {
  profile: CreatorProfile;
  items: T[];
  limit: number;
  offset: number;
}

export interface CreatorCourseProfileList extends CreatorProfileList<CreatorCourseItem> {
  total: number;
}

export type PublicationState = "unpublished" | "published" | "withdrawn";
export type InitialPublicationState = Exclude<PublicationState, "withdrawn">;

export type FederationMode = "off" | "followers";

export interface InitialPublicationInput {
  publication_state: InitialPublicationState;
  federation_mode: "off";
  index_eligible: false;
}

export interface RecipeCreatePayload extends Omit<
  RecipeWritePayload,
  "version"
> {
  initial_publication: InitialPublicationInput;
  creator_attribution?: RecipeCreatorAttribution;
}

export interface RecipePublicationInput {
  publication_state: PublicationState;
  federation_mode: FederationMode;
  index_eligible: false;
}

export interface RecipePublicationView extends RecipePublicationInput {
  recipe_id: string;
  updated_by: string | null;
  updated_at: string;
  published_at: string | null;
  visibility: Recipe["visibility"];
  public_instance: boolean;
  federation_enabled: boolean;
  federation_origin: string;
}

export type PublicRecipe = PublicRecipeDetail;

export interface RecipeRevision {
  id: string;
  version: number;
  title: string;
  editor_name: string;
  created_at: string;
}

export interface RecipeRevisionDiff {
  from_version: number;
  to_version: number;
  changes: Record<string, { before: unknown; after: unknown }>;
}

export interface Cookbook {
  id: string;
  title: string;
  description: string;
  visibility: "private" | "unlisted" | "public";
  owner_id: string;
  owner_name?: string;
  creator_attributions?: CreatorAttribution[];
  recipe_count?: number;
  created_at?: string;
  updated_at?: string;
}

export interface TrashedRecipe extends Recipe {
  deleted_at: string;
}

export interface AuditLogEntry {
  id: string;
  actor_name: string;
  action: string;
  resource_type: string;
  resource_id?: string;
  created_at: string;
  response_status: number;
}

export interface AdminInvitation {
  id: string;
  email: string;
  token?: string;
  expires_at: string;
}

export interface AdminInvitationCreated extends AdminInvitation {
  token: string;
}

export interface CookbookMember {
  user_id: string;
  display_name: string;
  email: string;
  role: "owner" | "editor" | "viewer";
}

export interface CookbookInvitation {
  id: string;
  email: string;
  role: "editor" | "viewer";
  token?: string;
  expires_at: string;
}

export function errorMessage(error: unknown, fallback: string): string {
  return error instanceof Error ? error.message : fallback;
}

export async function readJson<T>(response: Response): Promise<T> {
  return response.json() as Promise<T>;
}
