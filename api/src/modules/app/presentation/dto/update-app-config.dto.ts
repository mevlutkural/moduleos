import {
  IsOptional,
  IsNumber,
  IsString,
  IsArray,
  ValidateNested,
  Min,
  Max,
  IsIn,
  Matches,
  IsNotEmpty,
} from 'class-validator';
import { Type } from 'class-transformer';

class EnvVarDto {
  @IsString()
  key: string;

  @IsString()
  value: string;
}

export class UpdateAppConfigDto {
  @IsOptional()
  @IsNumber()
  @Min(1)
  @Max(65535)
  containerPort?: number;

  @IsOptional()
  @IsNumber()
  @Min(1)
  @Max(100)
  replicas?: number;

  @IsOptional()
  @IsString()
  @IsIn(['none', 'on-failure', 'any'])
  restartPolicy?: string;

  @IsOptional()
  @IsString()
  @Matches(/^\d+[mg]$/i, {
    message: 'Memory limit must be in format like "512m" or "2g"',
  })
  memoryLimit?: string | null;

  @IsOptional()
  @IsString()
  @Matches(/^\d+(\.\d+)?$/, {
    message: 'CPU limit must be a number like "0.5" or "2"',
  })
  cpuLimit?: string | null;

  @IsOptional()
  @IsArray()
  @ValidateNested({ each: true })
  @Type(() => EnvVarDto)
  envVars?: EnvVarDto[];

  @IsOptional()
  @IsString()
  @IsNotEmpty()
  image?: string;
}
