export interface ApiResponse<T> {
  message: string;
  content: T;
  timestamp: Date;
}

export interface PaginatedMeta {
  totalRecords: number;
  totalPages: number;
  currentPage: number;
  hasNextPage: boolean;
  hasPreviousPage: boolean;
}

export interface PaginatedList<T> {
  records: T[];
  meta: PaginatedMeta;
}

export type PaginatedResponse<T> = ApiResponse<PaginatedList<T>>;
