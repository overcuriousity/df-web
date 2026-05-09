-- web-animals: tame/owned creatures roster for the /therapist Animals view.
-- Invoked via: dfhack-run web-animals
-- Output: JSON array of {id, name, race, caste, age_tier, training, marked_for_slaughter, caged}.

local json = require('json')

local function df2utf(s) return dfhack.df2utf(s or '') end

local function try(f, default)
    local ok, v = pcall(f)
    if ok then return v end
    return default
end

local function age_tier(u)
    if u.flags1.tame == false and u.flags1.was_tame == false then
        -- still leave wild creatures filtered out below; this branch
        -- exists only to make intent clear.
    end
    if try(function() return u.profession == df.profession.BABY end, false) then return 'baby' end
    if try(function() return u.profession == df.profession.CHILD end, false) then return 'child' end
    return 'adult'
end

local function training_label(u)
    -- DF tracks training under u.training_level (an enum from dfhack-side)
    -- and u.flags1.tame/u.flags1.trained. Best-effort label.
    local lvl = try(function() return df.animal_training_level[u.training_level] end, nil)
    if lvl then return tostring(lvl) end
    if u.flags1.tame then return 'Tame' end
    return ''
end

local function species_name(u)
    return df2utf(try(function()
        local r = df.global.world.raws.creatures.all[u.race]
        return r and r.creature_id or ''
    end, '') or '')
end

local function caste_name(u)
    return df2utf(try(function()
        local r = df.global.world.raws.creatures.all[u.race]
        return r and r.caste[u.caste].caste_id or ''
    end, '') or '')
end

local function in_cage(u)
    return try(function() return u.flags1.caged or u.general_refs and false end, false) or false
end

local result = {}
for _, u in ipairs(df.global.world.units.active) do
    if dfhack.units.isAlive(u) and not dfhack.units.isCitizen(u) then
        local tame = try(function() return u.flags1.tame end, false)
        local owned = try(function() return u.flags1.has_owner end, false) or tame
        if tame or owned or u.flags1.caged then
            result[#result + 1] = {
                id                   = u.id,
                name                 = df2utf(dfhack.units.getReadableName(u)),
                race                 = species_name(u),
                caste                = caste_name(u),
                age_tier             = age_tier(u),
                training             = training_label(u),
                marked_for_slaughter = try(function() return u.flags2.slaughter end, false) or false,
                caged                = try(function() return u.flags1.caged end, false) or false,
            }
        end
    end
end

print(json.encode(result))
