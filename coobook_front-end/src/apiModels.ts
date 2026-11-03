export interface ApiFailure {
  error?: string;
  message?: string;
}

export interface UserSummary {
  id: string;
  email: string;
  display_name: string;
  is_admin: boolean;
  created_at: string;
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

export type AdminUser = UserSummary;

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
}

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

export interface Recipe {
  id: string;
  owner_id?: string;
  creator_name: string;
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

export type PublicationState = "unpublished" | "published" | "withdrawn";
export type InitialPublicationState = Exclude<PublicationState, "withdrawn">;

export type FederationMode = "off" | "followers";

export interface InitialPublicationInput {
  publication_state: InitialPublicationState;
  federation_mode: "off";
  index_eligible: false;
}

export interface RecipeCreatePayload extends Omit<RecipeWritePayload, "version"> {
  initial_publication: InitialPublicationInput;
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

export type PublicRecipe = Recipe;

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
