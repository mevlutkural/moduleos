export interface PaginationMeta {
  totalRecords: number;
  totalPages: number;
  currentPage: number;
  pageSize: number;
  hasNextPage: boolean;
  hasPreviousPage: boolean;
}

export interface PaginatedResult<T> {
  data: T[];
  meta: PaginationMeta;
}

export class PaginatedResultBuilder {
  static create<T>(
    data: T[],
    totalRecords: number,
    currentPage: number,
    pageSize: number,
  ): PaginatedResult<T> {
    const totalPages = Math.ceil(totalRecords / pageSize);
    const hasNextPage = currentPage < totalPages;
    const hasPreviousPage = currentPage > 1;

    return {
      data,
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
