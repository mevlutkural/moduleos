import { SelectQueryBuilder, Brackets, ObjectLiteral } from 'typeorm';
import { BaseQueryDto } from '../dto/base-query.dto';
import { BadRequestException } from '@nestjs/common';
import { TimeRangedQueryDto } from '../dto/time-ranged.query.dto';
import { I18nService } from 'nestjs-i18n';
import { FindAllOptions } from '../models/find-all-options.model';
import { TimeRangedFindAllOptions } from '../models/time-ranged-find-all-options.model';

export class TypeOrmQueryApplicator<Entity extends ObjectLiteral> {
  constructor(
    private readonly i18n: I18nService,
    private readonly builder: SelectQueryBuilder<Entity>,
    private readonly dto:
      | BaseQueryDto
      | TimeRangedQueryDto
      | FindAllOptions
      | TimeRangedFindAllOptions,
  ) {}

  applyPagination() {
    const page = this.dto.page || 1;
    const limit = this.dto.limit || 10;

    this.builder.skip((page - 1) * limit).take(limit);

    return this;
  }

  applySorting(allowedFields: string[] = []) {
    const sortRules = this.dto.orderBy;

    if (!sortRules || sortRules.length === 0) return this;

    sortRules.forEach((rule) => {
      if (allowedFields.length > 0 && !allowedFields.includes(rule.field)) {
        throw new BadRequestException(
          this.i18n.t('errorMessages.general.sortingByNotAllowed', {
            args: {
              field: rule.field,
              allowedFields: allowedFields.join(', '),
            },
          }),
        );
      }

      this.builder.addOrderBy(
        `${this.builder.alias}.${rule.field}`,
        rule.order,
      );
    });

    return this;
  }

  applyDateRange(dateColumn: string = 'created_at') {
    const hasDateRange = 'startDate' in this.dto || 'endDate' in this.dto;

    if (!hasDateRange) return this;

    if (this.dto.startDate) {
      this.builder.andWhere(
        `${this.builder.alias}.${dateColumn} >= :startDate`,
        {
          startDate: this.dto.startDate,
        },
      );
    }

    if (this.dto.endDate) {
      this.builder.andWhere(`${this.builder.alias}.${dateColumn} <= :endDate`, {
        endDate: this.dto.endDate,
      });
    }

    return this;
  }

  applySearch(searchFields: string[]) {
    const hasSearch = 'search' in this.dto && !!this.dto.search;

    if (!hasSearch || searchFields.length === 0) return this;

    const searchTerm = this.dto.search!;

    this.builder.andWhere(
      new Brackets((qb) => {
        searchFields.forEach((field) => {
          qb.orWhere(`${this.builder.alias}.${field} ILIKE :search`, {
            search: `%${searchTerm}%`,
          });
        });
      }),
    );

    return this;
  }
}
