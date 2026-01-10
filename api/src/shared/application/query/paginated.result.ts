export interface PaginationMeta {
  totalRecords: number;
  totalPages: number;
  currentPage: number;
  pageSize: number;
  hasNextPage: boolean;
  hasPreviousPage: boolean;
}

export interface PaginatedResult<T> {
  records: T[];
  meta: PaginationMeta;
}

export class PaginatedResultBuilder {
  static create<T>(
    records: T[],
    totalRecords: number,
    currentPage: number,
    pageSize: number,
  ): PaginatedResult<T> {
    const totalPages = Math.ceil(totalRecords / pageSize);
    const hasNextPage = currentPage < totalPages;
    const hasPreviousPage = currentPage > 1;

    return {
      records,
      meta: {
        totalRecords,
        totalPages,
        currentPage,
        pageSize,
        hasNextPage,
        hasPreviousPage,
      },
    };
  }
}
