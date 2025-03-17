// Layout data interface
export interface LayoutData {
  user: User;
  adminPath: string;
} 

export interface User {
  id: string;
  name: string;
  email: string;
  account: Account;
}

export interface Account {
  id: string;
  user_id: string;
  sessions: Session[];
  providers: Provider[];
  clients: Client[];
}

export interface Session {
  id: string;
  account_id: string;
  browser_info: string;
  access_token: string;
  expires?: string;
  is_online?: boolean;
}

export interface Provider {
  id: string;
  account_id: string;
  name: string;
  login: string;
  avatar_url: string;
  profile_url?: string;
  email?: string;
}

export interface Client {
  id: string;
  account_id: string;
  os: string;
  ip: string;
  hostname: string;
  expires?: string;
}

// Domain interfaces for proxy configuration
export interface Domain {
  id: string;
  name: string;
  account_id: string;
  created_at: string;
  updated_at: string;
}

export interface Certificate {
  id: string;
  domain_id: string;
  domain_name?: string; // For UI display purposes
  issued_at: string;
  expires_at: string;
  issuer: string;
  status: 'valid' | 'expired' | 'revoked';
}

export interface ProxyRoute {
  id: string;
  domain_id: string;
  domain_name?: string; // For UI display purposes
  container_id: string;
  container_ip: string;
  container_port: string;
  protocol: 'http' | 'https';
  path: string;
  active: boolean;
  created_at: string;
  updated_at: string;
}

// API Response interfaces
export interface ApiResponse {
  status: string;
  message: string;
}

// Container interfaces
export interface Container {
  id: string;
  name: string;
  image: string;
  state?: string;
  status: string;
  created: number | string;
  createdStr?: string;
  ports: PortMapping[] | string[];
  sizeRw?: number;
  sizeStr?: string;
  proxyPort?: string;
  env?: string[];
  ip?: string;
}

export interface PortMapping {
  IP: string;
  PrivatePort: number;
  PublicPort: number;
  Type: string;
}

export interface ContainerCreateParams {
  name: string;
  image: string;
  ports?: string[];
  volumes?: string[];
  environment?: string[];
  labels?: Record<string, string>;
  network?: string[];
  restart?: string;
}

export interface ContainerUpdateParams {
  name: string;
  image: string;
  ports?: string[];
  volumes?: string[];
  environment?: string[];
  labels?: Record<string, string>;
  network?: string[];
  restart?: string;
}

export interface ContainerCreateResponse {
  container: Container;
}

export interface ContainerUpdateResponse {
  container: Container;
}

// Image interfaces
export interface Image {
  id: string;
  shortId?: string;
  name: string;
  tag?: string;
  created: number | string;
  createdStr?: string;
  size: number | string;
  sizeStr?: string;
  repoDigests?: string[];
  repoTags?: string[];
}

export interface ImageUploadResponse {
  imageId: string;
}

// Authentication interfaces
export interface LoginCredentials {
  token?: string;
  username?: string;
  password?: string;
}

export interface AuthResponse {
  token?: string;
  user?: {
    username: string;
    roles: string[];
  };
  username: string;
  roles: string[];
}

export interface SessionResponse {
  authenticated: boolean;
  user?: {
    username: string;
    roles: string[];
  };
  username: string;
  roles: string[];
}

export interface SessionValidationResult {
  session: {
    id: string;
    userId: string;
    expiresAt: Date;
  } | null;
  user: {
    id: string;
    username: string;
  } | null;
}

