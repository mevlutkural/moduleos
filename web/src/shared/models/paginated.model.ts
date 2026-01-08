interface PaginatedMeta {
  totalRecords: number;
  totalPages: number;
  currentPage: number;
  hasNextPage: boolean;
  hasPreviousPage: boolean;
}

export interface Paginated<T> {
  records: T[];
  meta: PaginatedMeta;
}
